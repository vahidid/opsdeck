package dbx

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/stdlib"
)

func init() {
	// the driver logs dropped idle connections, which it retries anyway
	mysql.SetLogger(log.New(io.Discard, "", 0))
}

// sqlConn is a connection pool to one PostgreSQL or MySQL database.
type sqlConn struct {
	db       *sql.DB
	engine   string
	database string
	address  string
}

func openSQL(ctx context.Context, engine string, t target) (*sqlConn, error) {
	if t.user == "" {
		t.user = map[string]string{"postgres": "postgres", "mysql": "root"}[engine]
	}
	if t.database == "" && engine == "postgres" {
		t.database = "postgres"
	}
	var db *sql.DB
	switch engine {
	case "postgres":
		host, port, _ := net.SplitHostPort(t.addr)
		sslmode := map[string]string{"": "prefer", "prefer": "prefer", "require": "require", "verify-full": "verify-full", "disable": "disable"}[t.tls]
		if sslmode == "" {
			sslmode = "prefer"
		}
		cfg, err := pgx.ParseConfig(fmt.Sprintf("host=%s port=%s user=%s dbname=%s sslmode=%s connect_timeout=20 application_name=opsdeck",
			pgQuote(host), pgQuote(port), pgQuote(t.user), pgQuote(t.database), sslmode))
		if err != nil {
			return nil, err
		}
		cfg.Password = t.password
		cfg.DialFunc = pgconn.DialFunc(t.dial)
		db = stdlib.OpenDB(*cfg)
	case "mysql":
		mc := mysql.NewConfig()
		mc.User, mc.Passwd, mc.Net, mc.Addr, mc.DBName = t.user, t.password, "tcp", t.addr, t.database
		mc.Timeout = 20 * time.Second
		mc.DialFunc = t.dial
		mc.AllowNativePasswords = true
		mc.TLSConfig = map[string]string{"": "preferred", "prefer": "preferred", "require": "skip-verify", "verify-full": "true", "disable": "false"}[t.tls]
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
		return nil, err
	}
	return &sqlConn{db: db, engine: engine, database: t.database, address: t.addr}, nil
}

func pgQuote(s string) string {
	return "'" + strings.NewReplacer(`\`, `\\`, `'`, `\'`).Replace(s) + "'"
}

func (c *sqlConn) Engine() string   { return c.engine }
func (c *sqlConn) Database() string { return c.database }
func (c *sqlConn) close()           { c.db.Close() }

func (c *sqlConn) Info(ctx context.Context) (Info, error) {
	q := "select version(), current_user, current_database()"
	if c.engine == "mysql" {
		q = "select version(), current_user(), coalesce(database(), '')"
	}
	in := Info{Engine: c.engine, Address: c.address}
	err := c.db.QueryRowContext(ctx, q).Scan(&in.Version, &in.User, &in.Database)
	return in, err
}

func (c *sqlConn) Databases(ctx context.Context) ([]string, error) {
	q := "select datname from pg_database where not datistemplate and datallowconn order by 1"
	if c.engine == "mysql" {
		q = "show databases"
	}
	rows, err := c.db.QueryContext(ctx, q)
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

const maxTables = 5000

func (c *sqlConn) Tables(ctx context.Context, _ string) ([]Table, error) {
	q := `select n.nspname, c.relname, c.relkind::text, c.reltuples::bigint,
		case when c.relkind in ('r','m','p') then pg_total_relation_size(c.oid) else 0 end
	from pg_class c join pg_namespace n on n.oid = c.relnamespace
	where c.relkind in ('r','v','m','p','f') and n.nspname not in ('pg_catalog','information_schema')
		and n.nspname not like 'pg_toast%' and n.nspname not like 'pg_temp%'
	order by 1, 2 limit ` + strconv.Itoa(maxTables)
	if c.engine == "mysql" {
		q = `select table_schema, table_name, table_type, coalesce(table_rows, -1), coalesce(data_length + index_length, 0)
		from information_schema.tables where table_schema = database() order by 2 limit ` + strconv.Itoa(maxTables)
	}
	rows, err := c.db.QueryContext(ctx, q)
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
		name := c.ident(t.Name)
		if c.engine == "postgres" && t.Schema != "public" {
			name = c.ident(t.Schema) + "." + name
		}
		t.Query = "select * from " + name + " limit 100;"
		out = append(out, t)
	}
	return out, rows.Err()
}

var (
	plainIdent = regexp.MustCompile(`^[a-z_][a-z0-9_$]*$`)
	mysqlIdent = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_$]*$`)
)

// ident quotes a name only when it needs quotes.
func (c *sqlConn) ident(s string) string {
	if c.engine == "mysql" {
		if mysqlIdent.MatchString(s) {
			return s
		}
		return "`" + strings.ReplaceAll(s, "`", "``") + "`"
	}
	if plainIdent.MatchString(s) {
		return s
	}
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}

func (c *sqlConn) Table(ctx context.Context, schema, name string) (*TableInfo, error) {
	ti := &TableInfo{Schema: schema, Name: name, Kind: "table", Columns: []Column{}, Indexes: []Index{}}
	var colQ, idxQ string
	if c.engine == "postgres" {
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
	rows, err := c.db.QueryContext(ctx, colQ, schema, name)
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
	rows, err = c.db.QueryContext(ctx, idxQ, schema, name)
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

// Run runs a SQL script on one session, in one transaction: read-only (and
// rolled back) unless opts.Write; a change is committed only when every
// statement succeeded. It returns one result per statement run; a failed
// statement ends the script and its error is in its result.
func (c *sqlConn) Run(ctx context.Context, script string, opts QueryOptions) (cls Classification, results []Result, err error) {
	opts.defaults()
	cls = Classify(c.engine, script)
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
	ctx, cancel := context.WithTimeout(ctx, opts.Timeout)
	defer cancel()
	conn, err := c.db.Conn(ctx)
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
	if c.engine == "postgres" {
		tx.ExecContext(ctx, "set local statement_timeout = "+strconv.FormatInt(ms, 10))
	} else {
		conn.ExecContext(ctx, "set session max_execution_time = "+strconv.FormatInt(ms, 10))
	}
	results = []Result{}
	budget := maxResultBytes
	for _, st := range cls.Statements {
		res := Result{Statement: st.Text, RowsAffected: -1}
		start := time.Now()
		if st.ReturnsRows {
			err = c.query(ctx, tx, st.Text, opts, &budget, &res)
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

func (c *sqlConn) query(ctx context.Context, tx *sql.Tx, text string, opts QueryOptions, budget *int, res *Result) error {
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
		if len(res.Rows) >= opts.MaxRows || *budget <= 0 {
			res.Truncated = true
			break
		}
		if err := rows.Scan(ptrs...); err != nil {
			return err
		}
		row := make([]any, len(vals))
		for i, v := range vals {
			row[i] = displayValue(v, opts.CellLimit)
		}
		*budget -= size(row)
		res.Rows = append(res.Rows, row)
	}
	return rows.Err()
}
