package server

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"opsdeck/internal/connect"
	"opsdeck/internal/dbx"
	"opsdeck/internal/vault"
	"opsdeck/internal/workspace"
)

// dbRoutes: the Databases page. Reads run so they cannot change data (SQL:
// read-only transactions; MongoDB and Redis: only read operations); a script
// that is not a read answers 409 not_read until the user confirms it (write:
// true), and confirmed changes are audited.
func (s *Server) dbRoutes(mux *http.ServeMux, u func(http.HandlerFunc) http.HandlerFunc) {
	D := "/api/workspaces/{ws}/databases/{db}"
	mux.HandleFunc("GET "+D+"/info", u(s.dbRead(func(c *dbctx) (any, error) {
		in, err := c.conn.Info(c.ctx)
		if err != nil {
			return nil, err
		}
		env, protected := s.dbEnvironment(c.ws, c.res)
		return map[string]any{"info": in, "path": dbx.Path(c.ws, c.res), "environment": env, "protected": protected, "atomic": dbx.Atomic(c.conn.Engine())}, nil
	})))
	mux.HandleFunc("GET "+D+"/databases", u(s.dbRead(func(c *dbctx) (any, error) { return c.conn.Databases(c.ctx) })))
	mux.HandleFunc("GET "+D+"/tables", u(s.dbRead(func(c *dbctx) (any, error) { return c.conn.Tables(c.ctx, c.r.URL.Query().Get("match")) })))
	mux.HandleFunc("GET "+D+"/table", u(s.dbRead(func(c *dbctx) (any, error) {
		return c.conn.Table(c.ctx, c.r.URL.Query().Get("schema"), c.r.URL.Query().Get("name"))
	})))
	mux.HandleFunc("POST "+D+"/classify", u(func(w http.ResponseWriter, r *http.Request) {
		ws, res, ok := s.dbResource(w, r)
		if !ok {
			return
		}
		var in struct{ SQL string }
		if err := readJSON(r, &in); err != nil {
			fail(w, err)
			return
		}
		engine, err := dbx.Engine(res)
		if err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		env, protected := s.dbEnvironment(ws, res)
		cls := dbx.Classify(engine, in.SQL)
		writeJSON(w, 200, map[string]any{"risk": cls.Risk, "reasons": nonNil(cls.Reasons), "statements": len(cls.Statements), "environment": env, "protected": protected, "atomic": dbx.Atomic(engine)})
	}))
	mux.HandleFunc("POST "+D+"/query", u(s.dbQuery))
}

type dbctx struct {
	ctx  context.Context
	r    *http.Request
	ws   *workspace.Workspace
	res  *workspace.Resource
	conn dbx.Session
}

func (s *Server) dbResource(w http.ResponseWriter, r *http.Request) (*workspace.Workspace, *workspace.Resource, bool) {
	ws, ok := s.workspace(w, r)
	if !ok {
		return nil, nil, false
	}
	res := ws.Find("Database", r.PathValue("db"))
	if res == nil {
		writeErr(w, 404, "database not found")
		return nil, nil, false
	}
	return ws, res, true
}

func (s *Server) dbConn(w http.ResponseWriter, r *http.Request, ctx context.Context, database string) (*dbctx, bool) {
	ws, res, ok := s.dbResource(w, r)
	if !ok {
		return nil, false
	}
	conn, err := s.db.Conn(ctx, ws, res, database)
	if err != nil {
		dbFail(w, err)
		return nil, false
	}
	return &dbctx{ctx: ctx, r: r, ws: ws, res: res, conn: conn}, true
}

func (s *Server) dbRead(fn func(c *dbctx) (any, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
		defer cancel()
		c, ok := s.dbConn(w, r, ctx, r.URL.Query().Get("database"))
		if !ok {
			return
		}
		v, err := fn(c)
		if err != nil {
			dbFail(w, err)
			return
		}
		writeJSON(w, 200, v)
	}
}

// dbQuery runs a script. Reads answer with their results; a script that is
// not a read answers 409 until it is sent again with write: true. SQL errors
// are part of a 200 answer (with the results of the statements before).
func (s *Server) dbQuery(w http.ResponseWriter, r *http.Request) {
	var in struct {
		SQL      string `json:"sql"`
		Database string `json:"database"`
		Write    bool   `json:"write"`
		MaxRows  int    `json:"maxRows"`
	}
	if err := readJSON(r, &in); err != nil {
		fail(w, err)
		return
	}
	if in.MaxRows <= 0 || in.MaxRows > 10000 {
		in.MaxRows = 1000
	}
	ctx := r.Context()
	if in.Write {
		// a confirmed change completes even when the page goes away
		ctx = connect.WithActor(context.WithoutCancel(ctx), connect.Actor{Kind: "user"})
	}
	c, ok := s.dbConn(w, r, ctx, in.Database)
	if !ok {
		return
	}
	start := time.Now()
	// the page shows whole values (up to 64K characters) in its cell viewer
	cls, results, err := c.conn.Run(ctx, in.SQL, dbx.QueryOptions{Write: in.Write, MaxRows: in.MaxRows, CellLimit: 65536})
	var notRead *dbx.NotReadError
	if errors.As(err, &notRead) {
		env, protected := s.dbEnvironment(c.ws, c.res)
		writeJSON(w, 409, map[string]any{"error": err.Error(), "code": "not_read", "risk": cls.Risk, "reasons": nonNil(cls.Reasons),
			"statements": len(cls.Statements), "environment": env, "protected": protected, "atomic": dbx.Atomic(c.conn.Engine())})
		return
	}
	if in.Write {
		sql := in.SQL
		if len(sql) > 4000 {
			sql = sql[:4000] + "…"
		}
		s.record(r, c.ws.Name(), "db.query", c.res.ID(), map[string]any{"database": c.conn.Database(), "sql": sql, "risk": cls.Risk, "statements": len(cls.Statements)}, err)
	}
	out := map[string]any{"risk": cls.Risk, "reasons": nonNil(cls.Reasons), "results": results, "database": c.conn.Database(),
		"durationMs": time.Since(start).Milliseconds(), "committed": in.Write && err == nil}
	if err != nil {
		if results == nil { // nothing ran: no statement, a lost connection
			dbFail(w, err)
			return
		}
		out["error"] = err.Error()
	}
	writeJSON(w, 200, out)
}

func (s *Server) dbEnvironment(ws *workspace.Workspace, r *workspace.Resource) (string, bool) {
	e := ws.EnvironmentOf(r)
	return e.Name, e.Protected
}

func dbFail(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, vault.ErrLocked), errors.Is(err, vault.ErrNotInitialized):
		fail(w, err)
	case errors.Is(err, dbx.ErrUnsupported):
		writeJSON(w, 400, apiError{Error: err.Error(), Code: "unsupported_engine"})
	case strings.Contains(err.Error(), "vault"):
		fail(w, err)
	default:
		writeErr(w, 400, err.Error())
	}
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
