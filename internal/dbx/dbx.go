// Package dbx connects to the PostgreSQL and MySQL/MariaDB databases of a
// workspace: through the database's via chain (VPN / jump host), or through
// an SSH tunnel to the host it runs on, with the login from the vault. Reads
// run in read-only transactions; changes run in a transaction of their own
// after the caller confirmed them.
package dbx

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/go-sql-driver/mysql"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"

	"opsdeck/internal/connect"
	"opsdeck/internal/vault"
	"opsdeck/internal/workspace"
)

func init() {
	// the driver logs dropped idle connections, which it retries anyway
	mysql.SetLogger(log.New(io.Discard, "", 0))
}

// ErrUnsupported is returned for engines opsdeck cannot open yet.
var ErrUnsupported = errors.New("opsdeck can open PostgreSQL and MySQL/MariaDB databases; this engine is not supported yet")

// Engine returns "postgres" or "mysql" for a Database resource.
func Engine(r *workspace.Resource) (string, error) {
	switch strings.ToLower(r.Str("engine")) {
	case "postgres", "postgresql":
		return "postgres", nil
	case "mysql", "mariadb":
		return "mysql", nil
	}
	return "", ErrUnsupported
}

type Manager struct {
	hub   *connect.Hub
	vault *vault.Vault

	mu    sync.Mutex
	conns map[string]*Conn
}

// Conn is a connection pool to one database of a Database resource.
type Conn struct {
	DB       *sql.DB
	Engine   string
	Database string
	Address  string
	User     string
	rev      string
}

func New(hub *connect.Hub, v *vault.Vault) *Manager {
	m := &Manager{hub: hub, vault: v, conns: map[string]*Conn{}}
	v.OnLock(m.Reset)
	return m
}

// Reset closes every pool (the vault locked: logins are gone).
func (m *Manager) Reset() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, c := range m.conns {
		c.DB.Close()
	}
	m.conns = map[string]*Conn{}
}

// Conn returns a pool for a database of r ("" = the resource's database).
func (m *Manager) Conn(ctx context.Context, w *workspace.Workspace, r *workspace.Resource, database string) (*Conn, error) {
	if r.Kind != "Database" {
		return nil, fmt.Errorf("%s is not a database", r.ID())
	}
	engine, err := Engine(r)
	if err != nil {
		return nil, err
	}
	user, password, rev := "", "", ""
	if ref := r.Str("login"); ref != "" {
		it, _, err := m.vault.ResolveFor(w.Name(), ref)
		if err != nil {
			return nil, err
		}
		if a := connect.ActorFrom(ctx); a.Kind == "agent" && !it.Policy.AgentUse {
			return nil, fmt.Errorf("credential %s is not allowed for agent use", ref)
		}
		user, password, rev = it.Get("username"), it.Get("password"), fmt.Sprintf("%s:%d", it.ID, it.Revision)
	}
	if user == "" {
		user = map[string]string{"postgres": "postgres", "mysql": "root"}[engine]
	}
	addr := r.Str("address")
	if addr == "" {
		return nil, fmt.Errorf("%s has no address", r.ID())
	}
	if _, _, err := net.SplitHostPort(addr); err != nil {
		addr = net.JoinHostPort(addr, map[string]string{"postgres": "5432", "mysql": "3306"}[engine])
	}
	if database == "" {
		database = r.Str("database")
	}
	if database == "" && engine == "postgres" {
		database = "postgres"
	}
	tlsMode := r.Str("tls")
	key := w.Name() + "/" + r.Name + "/" + database
	rev = strings.Join([]string{rev, addr, user, r.Via, r.Str("host"), tlsMode}, "|")
	m.mu.Lock()
	if c, ok := m.conns[key]; ok {
		if c.rev == rev {
			m.mu.Unlock()
			return c, nil
		}
		c.DB.Close()
		delete(m.conns, key)
	}
	m.mu.Unlock()

	// Each new connection asks the hub for the path: SSH clients and VPNs
	// are cached there and come back after a reconnect.
	dial := func(ctx context.Context, network, address string) (net.Conn, error) {
		d, err := m.dialer(ctx, w, r)
		if err != nil {
			return nil, err
		}
		return d(ctx, "tcp", address)
	}
	var db *sql.DB
	switch engine {
	case "postgres":
		host, port, _ := net.SplitHostPort(addr)
		sslmode := map[string]string{"": "prefer", "prefer": "prefer", "require": "require", "verify-full": "verify-full", "disable": "disable"}[tlsMode]
		if sslmode == "" {
			sslmode = "prefer"
		}
		cfg, err := pgx.ParseConfig(fmt.Sprintf("host=%s port=%s user=%s dbname=%s sslmode=%s connect_timeout=20 application_name=opsdeck",
			pgQuote(host), pgQuote(port), pgQuote(user), pgQuote(database), sslmode))
		if err != nil {
			return nil, err
		}
		cfg.Password = password
		cfg.DialFunc = dial
		db = stdlib.OpenDB(*cfg)
	case "mysql":
		mc := mysql.NewConfig()
		mc.User, mc.Passwd, mc.Net, mc.Addr, mc.DBName = user, password, "tcp", addr, database
		mc.Timeout = 20 * time.Second
		mc.DialFunc = dial
		mc.AllowNativePasswords = true
		mc.TLSConfig = map[string]string{"": "preferred", "prefer": "preferred", "require": "skip-verify", "verify-full": "true", "disable": "false"}[tlsMode]
		connector, err := mysql.NewConnector(mc)
		if err != nil {
			return nil, err
		}
		db = sql.OpenDB(connector)
	}
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(2)
	db.SetConnMaxIdleTime(5 * time.Minute)
	db.SetConnMaxLifetime(30 * time.Minute)
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("connect to %s (%s): %w", r.ID(), addr, err)
	}
	c := &Conn{DB: db, Engine: engine, Database: database, Address: addr, User: user, rev: rev}
	m.mu.Lock()
	if old, ok := m.conns[key]; ok {
		old.DB.Close()
	}
	m.conns[key] = c
	m.mu.Unlock()
	return c, nil
}

// dialer reaches a database: through its via chain, else through an SSH
// tunnel to the host it runs on (the address as that host sees it, so
// 127.0.0.1:5432 works), else directly.
func (m *Manager) dialer(ctx context.Context, w *workspace.Workspace, r *workspace.Resource) (connect.DialFunc, error) {
	if r.Via != "" {
		return m.hub.Dialer(ctx, w, r)
	}
	if name := r.Str("host"); name != "" {
		h := w.Find("Host", name)
		if h == nil {
			return nil, fmt.Errorf("%s: host %q not found", r.ID(), name)
		}
		client, err := m.hub.SSH(ctx, w, h)
		if err != nil {
			return nil, fmt.Errorf("host %s: %w", h.Name, err)
		}
		return client.DialContext, nil
	}
	var d net.Dialer
	return d.DialContext, nil
}

// Path describes how a database is reached, e.g. ["Host/bastion", "Host/db-1"].
func Path(w *workspace.Workspace, r *workspace.Resource) []string {
	if r.Via != "" {
		p, _ := connect.DescribePath(w, r)
		return p
	}
	if name := r.Str("host"); name != "" {
		if h := w.Find("Host", name); h != nil {
			p, _ := connect.DescribePath(w, h)
			return append(p, h.ID())
		}
	}
	return []string{}
}

func pgQuote(s string) string {
	return "'" + strings.NewReplacer(`\`, `\\`, `'`, `\'`).Replace(s) + "'"
}

// ---------------------------------------------------------------------------
// Catalog

type Info struct {
	Engine   string `json:"engine"`
	Version  string `json:"version"`
	User     string `json:"user"`
	Database string `json:"database"`
	Address  string `json:"address"`
}

func (c *Conn) Info(ctx context.Context) (Info, error) {
	q := "select version(), current_user, current_database()"
	if c.Engine == "mysql" {
		q = "select version(), current_user(), coalesce(database(), '')"
	}
	in := Info{Engine: c.Engine, Address: c.Address}
	err := c.DB.QueryRowContext(ctx, q).Scan(&in.Version, &in.User, &in.Database)
	return in, err
}

// Databases lists the databases the login can see.
func (c *Conn) Databases(ctx context.Context) ([]string, error) {
	q := "select datname from pg_database where not datistemplate and datallowconn order by 1"
	if c.Engine == "mysql" {
		q = "show databases"
	}
	return c.strings(ctx, q)
}

func (c *Conn) strings(ctx context.Context, q string, args ...any) ([]string, error) {
	rows, err := c.DB.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

type Table struct {
	Schema string `json:"schema"`
	Name   string `json:"name"`
	Type   string `json:"type"`  // table | view | materialized view | partitioned table | foreign table
	Rows   int64  `json:"rows"`  // estimate, -1 = unknown
	Bytes  int64  `json:"bytes"` // tables and indexes
}

const maxTables = 5000

// Tables lists the tables and views of the database (system schemas left out).
func (c *Conn) Tables(ctx context.Context) ([]Table, error) {
	q := `select n.nspname, c.relname, c.relkind::text, c.reltuples::bigint,
		case when c.relkind in ('r','m','p') then pg_total_relation_size(c.oid) else 0 end
	from pg_class c join pg_namespace n on n.oid = c.relnamespace
	where c.relkind in ('r','v','m','p','f') and n.nspname not in ('pg_catalog','information_schema')
		and n.nspname not like 'pg_toast%' and n.nspname not like 'pg_temp%'
	order by 1, 2 limit ` + strconv.Itoa(maxTables)
	if c.Engine == "mysql" {
		q = `select table_schema, table_name, table_type, coalesce(table_rows, -1), coalesce(data_length + index_length, 0)
		from information_schema.tables where table_schema = database() order by 2 limit ` + strconv.Itoa(maxTables)
	}
	rows, err := c.DB.QueryContext(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	kinds := map[string]string{"r": "table", "v": "view", "m": "materialized view", "p": "partitioned table", "f": "foreign table",
		"BASE TABLE": "table", "VIEW": "view", "SYSTEM VIEW": "view", "SEQUENCE": "sequence"}
	out := []Table{}
	for rows.Next() {
		var t Table
		if err := rows.Scan(&t.Schema, &t.Name, &t.Type, &t.Rows, &t.Bytes); err != nil {
			return nil, err
		}
		if k, ok := kinds[t.Type]; ok {
			t.Type = k
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

type Column struct {
	Name     string  `json:"name"`
	Type     string  `json:"type"`
	Nullable bool    `json:"nullable"`
	Default  *string `json:"default"`
	Primary  bool    `json:"primary"`
}

type Index struct {
	Name       string `json:"name"`
	Definition string `json:"definition"`
	Unique     bool   `json:"unique"`
	Primary    bool   `json:"primary"`
}

type TableInfo struct {
	Schema  string   `json:"schema"`
	Name    string   `json:"name"`
	Columns []Column `json:"columns"`
	Indexes []Index  `json:"indexes"`
}

// Table describes a table or view: its columns and indexes.
func (c *Conn) Table(ctx context.Context, schema, name string) (*TableInfo, error) {
	ti := &TableInfo{Schema: schema, Name: name, Columns: []Column{}, Indexes: []Index{}}
	var colQ, idxQ string
	if c.Engine == "postgres" {
		rel := `(select c.oid from pg_class c join pg_namespace n on n.oid = c.relnamespace where n.nspname = $1 and c.relname = $2)`
		colQ = `select a.attname, format_type(a.atttypid, a.atttypmod), not a.attnotnull, pg_get_expr(d.adbin, d.adrelid),
			coalesce((select true from pg_index i where i.indrelid = a.attrelid and i.indisprimary and a.attnum = any(i.indkey)), false)
		from pg_attribute a left join pg_attrdef d on d.adrelid = a.attrelid and d.adnum = a.attnum
		where a.attrelid = ` + rel + ` and a.attnum > 0 and not a.attisdropped order by a.attnum`
		idxQ = `select i.relname, pg_get_indexdef(ix.indexrelid), ix.indisunique, ix.indisprimary
		from pg_index ix join pg_class i on i.oid = ix.indexrelid where ix.indrelid = ` + rel + ` order by ix.indisprimary desc, 1`
	} else {
		colQ = `select column_name, column_type, is_nullable = 'YES', column_default, column_key = 'PRI'
		from information_schema.columns where table_schema = ? and table_name = ? order by ordinal_position`
		idxQ = `select index_name, concat('(', group_concat(column_name order by seq_in_index separator ', '), ')'), min(non_unique) = 0, index_name = 'PRIMARY'
		from information_schema.statistics where table_schema = ? and table_name = ? group by index_name order by index_name = 'PRIMARY' desc, 1`
	}
	rows, err := c.DB.QueryContext(ctx, colQ, schema, name)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var col Column
		if err := rows.Scan(&col.Name, &col.Type, &col.Nullable, &col.Default, &col.Primary); err != nil {
			rows.Close()
			return nil, err
		}
		ti.Columns = append(ti.Columns, col)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(ti.Columns) == 0 {
		return nil, fmt.Errorf("table %s.%s not found", schema, name)
	}
	rows, err = c.DB.QueryContext(ctx, idxQ, schema, name)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var ix Index
		if err := rows.Scan(&ix.Name, &ix.Definition, &ix.Unique, &ix.Primary); err != nil {
			return nil, err
		}
		ti.Indexes = append(ti.Indexes, ix)
	}
	return ti, rows.Err()
}

// QuoteIdent quotes a schema, table or column name for the engine.
func (c *Conn) QuoteIdent(s string) string {
	if c.Engine == "mysql" {
		return "`" + strings.ReplaceAll(s, "`", "``") + "`"
	}
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}

// ---------------------------------------------------------------------------
// Queries

// NotReadError is returned when SQL that is not a read is run as a read.
type NotReadError struct{ Classification }

func (e *NotReadError) Error() string {
	return "this SQL is not a read (" + strings.Join(e.Reasons, "; ") + "): confirm it to run it as a change"
}

type QueryOptions struct {
	Write   bool          // run statements that are not reads (the caller confirmed them)
	MaxRows int           // per result (default 500)
	Timeout time.Duration // per script (default 2 minutes)
}

type ResultColumn struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

type Result struct {
	Statement    string         `json:"statement"`
	Columns      []ResultColumn `json:"columns,omitempty"`
	Rows         [][]any        `json:"rows,omitempty"`
	Truncated    bool           `json:"truncated,omitempty"`
	RowsAffected int64          `json:"rowsAffected"` // -1 for result sets
	DurationMs   int64          `json:"durationMs"`
	Error        string         `json:"error,omitempty"`
}

// Run runs a SQL script on one session, in one transaction: read-only (and
// rolled back) unless opts.Write; a change is committed only when every
// statement succeeded. It returns one result per statement run; a failed
// statement ends the script and its error is in its result.
func (c *Conn) Run(ctx context.Context, script string, opts QueryOptions) (cls Classification, results []Result, err error) {
	cls = Classify(c.Engine, script)
	if len(cls.Statements) == 0 {
		return cls, nil, errors.New("no SQL statement")
	}
	if !opts.Write && cls.Risk != RiskRead {
		return cls, nil, &NotReadError{cls}
	}
	for _, st := range cls.Statements {
		if st.TxControl {
			return cls, nil, fmt.Errorf("%q: opsdeck runs each script as one transaction; leave out BEGIN, COMMIT and ROLLBACK", st.Text)
		}
	}
	if opts.MaxRows <= 0 {
		opts.MaxRows = 500
	}
	if opts.Timeout <= 0 {
		opts.Timeout = 2 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, opts.Timeout)
	defer cancel()
	conn, err := c.DB.Conn(ctx)
	if err != nil {
		return cls, nil, err
	}
	defer conn.Close()
	tx, err := conn.BeginTx(ctx, &sql.TxOptions{ReadOnly: !opts.Write})
	if err != nil {
		return cls, nil, err
	}
	committed := false
	defer func() {
		if !committed {
			tx.Rollback()
		}
	}()
	// the server stops a statement that runs too long, also when the
	// connection to opsdeck is gone
	ms := opts.Timeout.Milliseconds()
	if c.Engine == "postgres" {
		tx.ExecContext(ctx, "set local statement_timeout = "+strconv.FormatInt(ms, 10))
	} else {
		conn.ExecContext(ctx, "set session max_execution_time = "+strconv.FormatInt(ms, 10))
	}
	results = []Result{}
	for _, st := range cls.Statements {
		res := Result{Statement: st.Text, RowsAffected: -1}
		start := time.Now()
		if st.ReturnsRows {
			err = c.query(ctx, tx, st.Text, opts.MaxRows, &res)
		} else {
			var r sql.Result
			if r, err = tx.ExecContext(ctx, st.Text); err == nil {
				res.RowsAffected, _ = r.RowsAffected()
			}
		}
		res.DurationMs = time.Since(start).Milliseconds()
		if err != nil {
			res.Error = err.Error()
			results = append(results, res)
			if opts.Write {
				return cls, results, fmt.Errorf("%w (nothing was committed)", err)
			}
			return cls, results, err
		}
		results = append(results, res)
	}
	if opts.Write {
		if err := tx.Commit(); err != nil {
			return cls, results, err
		}
		committed = true
	}
	return cls, results, nil
}

func (c *Conn) query(ctx context.Context, tx *sql.Tx, text string, maxRows int, res *Result) error {
	rows, err := tx.QueryContext(ctx, text)
	if err != nil {
		return err
	}
	defer rows.Close()
	types, err := rows.ColumnTypes()
	if err != nil {
		return err
	}
	res.Columns = make([]ResultColumn, len(types))
	for i, t := range types {
		res.Columns[i] = ResultColumn{Name: t.Name(), Type: strings.ToLower(t.DatabaseTypeName())}
	}
	res.Rows = [][]any{}
	vals := make([]any, len(types))
	ptrs := make([]any, len(types))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	for rows.Next() {
		if len(res.Rows) >= maxRows {
			res.Truncated = true
			break
		}
		if err := rows.Scan(ptrs...); err != nil {
			return err
		}
		row := make([]any, len(vals))
		for i, v := range vals {
			row[i] = displayValue(v)
		}
		res.Rows = append(res.Rows, row)
	}
	return rows.Err()
}

const maxCell = 10000

// displayValue turns a driver value into JSON: text (cut at 10,000
// characters), numbers, booleans, times as RFC 3339, binary as \x hex.
func displayValue(v any) any {
	switch x := v.(type) {
	case nil, bool, int64, float64, int32, int, float32:
		return x
	case time.Time:
		return x.Format(time.RFC3339Nano)
	case []byte:
		if utf8.Valid(x) {
			return cut(string(x))
		}
		if len(x) > maxCell/2 {
			return `\x` + hex.EncodeToString(x[:maxCell/2]) + "…"
		}
		return `\x` + hex.EncodeToString(x)
	case string:
		return cut(x)
	}
	return cut(fmt.Sprint(v))
}

func cut(s string) string {
	if len(s) <= maxCell {
		return s
	}
	r := []rune(s)
	if len(r) <= maxCell {
		return s
	}
	return string(r[:maxCell]) + "…"
}
