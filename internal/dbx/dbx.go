// Package dbx connects to the databases of a workspace — PostgreSQL,
// MySQL/MariaDB, MongoDB and Redis — through the database's via chain (VPN /
// jump host), or through an SSH tunnel to the host it runs on, with the
// login from the vault. Reads run so the server cannot change data (a
// read-only transaction, or only read operations); changes run after the
// caller confirmed them.
package dbx

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"opsdeck/internal/connect"
	"opsdeck/internal/vault"
	"opsdeck/internal/workspace"
)

// ErrUnsupported is returned for engines opsdeck cannot open yet.
var ErrUnsupported = errors.New("opsdeck can open PostgreSQL, MySQL/MariaDB, MongoDB and Redis databases; this engine is not supported yet")

// Engine returns postgres, mysql, mongodb or redis for a Database resource.
func Engine(r *workspace.Resource) (string, error) {
	switch strings.ToLower(r.Str("engine")) {
	case "postgres", "postgresql":
		return "postgres", nil
	case "mysql", "mariadb":
		return "mysql", nil
	case "mongodb", "mongo":
		return "mongodb", nil
	case "redis", "valkey":
		return "redis", nil
	}
	return "", ErrUnsupported
}

// Classify splits a script into statements and classifies each, for the
// engine: SQL, MongoDB shell commands or Redis commands.
func Classify(engine, script string) Classification {
	switch engine {
	case "mongodb":
		return classifyMongo(script)
	case "redis":
		return classifyRedis(script)
	}
	return classifySQL(engine, script)
}

// Atomic reports whether a script of the engine runs as one transaction.
func Atomic(engine string) bool { return engine == "postgres" || engine == "mysql" }

// Session is an open database of a Database resource.
type Session interface {
	Engine() string
	Database() string
	Info(ctx context.Context) (Info, error)
	// Databases lists the databases on the server (Redis: the numbered ones).
	Databases(ctx context.Context) ([]string, error)
	// Tables lists tables and views, collections, or keys (match filters Redis keys).
	Tables(ctx context.Context, match string) ([]Table, error)
	// Table describes one: columns and indexes, inferred fields, or a key.
	Table(ctx context.Context, schema, name string) (*TableInfo, error)
	// Run runs a script: reads only unless opts.Write.
	Run(ctx context.Context, script string, opts QueryOptions) (Classification, []Result, error)
	close()
}

type Manager struct {
	hub   *connect.Hub
	vault *vault.Vault

	mu    sync.Mutex
	conns map[string]cached
}

type cached struct {
	s   Session
	rev string
}

func New(hub *connect.Hub, v *vault.Vault) *Manager {
	m := &Manager{hub: hub, vault: v, conns: map[string]cached{}}
	v.OnLock(m.Reset)
	return m
}

// Reset closes every connection (the vault locked: logins are gone).
func (m *Manager) Reset() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, c := range m.conns {
		c.s.close()
	}
	m.conns = map[string]cached{}
}

// target is what a session needs to connect.
type target struct {
	res      *workspace.Resource
	addr     string
	user     string
	password string
	database string
	tls      string
	dial     connect.DialFunc
}

var defaultPorts = map[string]string{"postgres": "5432", "mysql": "3306", "mongodb": "27017", "redis": "6379"}

// Conn returns a session for a database of r ("" = the resource's database).
func (m *Manager) Conn(ctx context.Context, w *workspace.Workspace, r *workspace.Resource, database string) (Session, error) {
	if r.Kind != "Database" {
		return nil, fmt.Errorf("%s is not a database", r.ID())
	}
	engine, err := Engine(r)
	if err != nil {
		return nil, err
	}
	t := target{res: r, tls: r.Str("tls")}
	rev := ""
	if ref := r.Str("login"); ref != "" {
		it, _, err := m.vault.ResolveFor(w.Name(), ref)
		if err != nil {
			return nil, err
		}
		if a := connect.ActorFrom(ctx); a.Kind == "agent" && !it.Policy.AgentUse {
			return nil, fmt.Errorf("credential %s is not allowed for agent use", ref)
		}
		t.user, t.password, rev = it.Get("username"), it.Get("password"), fmt.Sprintf("%s:%d", it.ID, it.Revision)
	}
	t.addr = r.Str("address")
	if t.addr == "" {
		return nil, fmt.Errorf("%s has no address", r.ID())
	}
	if _, _, err := net.SplitHostPort(t.addr); err != nil {
		t.addr = net.JoinHostPort(t.addr, defaultPorts[engine])
	}
	t.database = database
	if t.database == "" {
		t.database = r.Str("database")
	}
	key := w.Name() + "/" + r.Name + "/" + t.database
	rev = strings.Join([]string{rev, t.addr, r.Via, r.Str("host"), t.tls, r.Str("authSource")}, "|")
	m.mu.Lock()
	if c, ok := m.conns[key]; ok {
		if c.rev == rev {
			m.mu.Unlock()
			return c.s, nil
		}
		c.s.close()
		delete(m.conns, key)
	}
	m.mu.Unlock()

	// Each new connection asks the hub for the path: SSH clients and VPNs
	// are cached there and come back after a reconnect.
	t.dial = func(ctx context.Context, network, address string) (net.Conn, error) {
		d, err := m.dialer(ctx, w, r)
		if err != nil {
			return nil, err
		}
		c, err := d(ctx, "tcp", address)
		if err != nil {
			return nil, err
		}
		return withDeadlines(c), nil
	}
	var s Session
	switch engine {
	case "postgres", "mysql":
		s, err = openSQL(ctx, engine, t)
	case "mongodb":
		s, err = openMongo(ctx, t)
	case "redis":
		s, err = openRedis(ctx, t)
	}
	if err != nil {
		return nil, fmt.Errorf("connect to %s (%s): %w", r.ID(), t.addr, err)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if old, ok := m.conns[key]; ok {
		// another request opened it meanwhile (the page asks for info and
		// tables at once): use that one, it may be in use already
		if old.rev == rev {
			s.close()
			return old.s, nil
		}
		old.s.close()
	}
	m.conns[key] = cached{s, rev}
	return s, nil
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

// withDeadlines gives a connection that cannot have deadlines (an SSH
// channel) ones, by putting a pipe in front of it: the MongoDB and Redis
// drivers set a deadline for every operation.
func withDeadlines(c net.Conn) net.Conn {
	if c.SetDeadline(time.Time{}) == nil {
		return c
	}
	near, far := net.Pipe()
	go func() {
		io.Copy(far, c)
		far.Close()
	}()
	go func() {
		io.Copy(c, far)
		c.Close()
	}()
	return pipeConn{near, c}
}

// pipeConn is the near end of the pipe, with the addresses of the connection.
type pipeConn struct {
	net.Conn
	c net.Conn
}

func (p pipeConn) LocalAddr() net.Addr  { return p.c.LocalAddr() }
func (p pipeConn) RemoteAddr() net.Addr { return p.c.RemoteAddr() }

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

// ---------------------------------------------------------------------------
// What sessions return

type Info struct {
	Engine   string `json:"engine"`
	Version  string `json:"version"`
	User     string `json:"user"`
	Database string `json:"database"`
	Address  string `json:"address"`
}

// Table is a table or view, a collection, or a Redis key.
type Table struct {
	Schema string `json:"schema"`
	Name   string `json:"name"`
	Type   string `json:"type"`  // table | view | … | collection | string, hash, list, set, zset, stream
	Rows   int64  `json:"rows"`  // estimate, -1 = unknown
	Bytes  int64  `json:"bytes"` // 0 = unknown
	Query  string `json:"query"` // what shows its content
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

type Property struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type TableInfo struct {
	Schema     string     `json:"schema"`
	Name       string     `json:"name"`
	Kind       string     `json:"kind"` // table | collection | key
	Columns    []Column   `json:"columns"`
	Indexes    []Index    `json:"indexes"`
	Properties []Property `json:"properties,omitempty"`
	Note       string     `json:"note,omitempty"` // e.g. "fields of 200 sampled documents"
}

// NotReadError is returned when a script that is not a read is run as one.
type NotReadError struct{ Classification }

func (e *NotReadError) Error() string {
	return "this is not a read (" + strings.Join(e.Reasons, "; ") + "): confirm it to run it as a change"
}

type QueryOptions struct {
	Write     bool          // run statements that are not reads (the caller confirmed them)
	MaxRows   int           // per result (default 500)
	Timeout   time.Duration // per script (default 2 minutes)
	CellLimit int           // characters per value (default 10,000)
}

func (o *QueryOptions) defaults() {
	if o.MaxRows <= 0 {
		o.MaxRows = 500
	}
	if o.Timeout <= 0 {
		o.Timeout = 2 * time.Minute
	}
	if o.CellLimit <= 0 {
		o.CellLimit = 10000
	}
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
	Note         string         `json:"note,omitempty"` // e.g. "next SCAN cursor: 1536"
}

// maxResultBytes bounds what one script returns, whatever the row limit.
const maxResultBytes = 32 << 20

// displayValue turns a driver value into JSON: text (cut at limit
// characters), numbers, booleans, times as RFC 3339, binary as \x hex;
// nested documents stay JSON.
func displayValue(v any, limit int) any {
	switch x := v.(type) {
	case nil, bool, int64, float64, int32, int, float32, json.RawMessage:
		return x
	case time.Time:
		return x.Format(time.RFC3339Nano)
	case []byte:
		if utf8.Valid(x) {
			return cut(string(x), limit)
		}
		if len(x) > limit/2 {
			return `\x` + hex.EncodeToString(x[:limit/2]) + "…"
		}
		return `\x` + hex.EncodeToString(x)
	case string:
		return cut(x, limit)
	}
	return cut(fmt.Sprint(v), limit)
}

func cut(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	r := []rune(s)
	if len(r) <= limit {
		return s
	}
	return string(r[:limit]) + "…"
}

// size estimates the bytes of a row, for maxResultBytes.
func size(row []any) int {
	n := 0
	for _, v := range row {
		switch x := v.(type) {
		case string:
			n += len(x)
		case json.RawMessage:
			n += len(x)
		default:
			n += 8
		}
	}
	return n
}
