package server

import (
	"fmt"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"opsdeck/internal/paths"
	"opsdeck/internal/testutil/fakellm"
	"opsdeck/internal/testutil/sshd"
)

// TestDatabasesLive drives the Databases API against real servers, reached
// through an SSH tunnel to a test host. Set OPSDECK_TEST_POSTGRES,
// OPSDECK_TEST_MYSQL, OPSDECK_TEST_MONGO and/or OPSDECK_TEST_REDIS
// (comma-separated) to user:password@127.0.0.1:port/db.
func TestDatabasesLive(t *testing.T) {
	targets := map[string][]string{}
	for engine, env := range map[string]string{"postgres": "OPSDECK_TEST_POSTGRES", "mysql": "OPSDECK_TEST_MYSQL",
		"mongodb": "OPSDECK_TEST_MONGO", "redis": "OPSDECK_TEST_REDIS"} {
		if v := os.Getenv(env); v != "" {
			targets[engine] = strings.Split(v, ",")
		}
	}
	if len(targets) == 0 {
		t.Skip("set OPSDECK_TEST_POSTGRES / _MYSQL / _MONGO / _REDIS to user:password@host:port/db")
	}
	t.Setenv("OPSDECK_HOME", t.TempDir())
	srv, err := New(paths.Default(), Options{Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	jar, _ := cookiejar.New(nil)
	c := &client{t: t, base: ts.URL, http: &http.Client{Jar: jar, Timeout: 3 * time.Minute}}
	c.must("POST", "/api/auth/login", map[string]string{"token": srv.token}, nil)
	c.must("POST", "/api/vault/init", map[string]string{"password": "correct horse battery"}, nil)
	c.must("POST", "/api/workspaces", map[string]string{"name": "acme"}, nil)
	var key map[string]any
	c.must("POST", "/api/vault/ssh/generate", map[string]any{"scope": "acme", "name": "k", "algorithm": "ed25519"}, &key)
	pub, _, _, _, _ := ssh.ParseAuthorizedKey([]byte(key["publicKey"].(string)))
	host, err := sshd.Start("127.0.0.1:0", sshd.Options{AuthorizedKeys: []ssh.PublicKey{pub}, Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close()
	_, port, _ := net.SplitHostPort(host.Addr)
	p, _ := strconv.Atoi(port)
	c.must("POST", "/api/workspaces/acme/resources", map[string]any{"kind": "Host", "name": "db-host",
		"spec": map[string]any{"address": "127.0.0.1", "port": p, "auth": "vault://acme/k"}}, nil)

	n := 0
	for engine, list := range targets {
		for _, raw := range list {
			n++
			u, err := url.Parse("x://" + raw)
			if err != nil {
				t.Fatal(err)
			}
			pw, _ := u.User.Password()
			name := fmt.Sprintf("db%d", n)
			login := "login-" + name
			c.must("POST", "/api/vault/items", map[string]any{"scope": "acme", "type": "login", "name": login, "fields": []map[string]any{
				{"name": "username", "value": u.User.Username(), "secret": false}, {"name": "password", "value": pw, "secret": true}}}, nil)
			c.must("POST", "/api/workspaces/acme/resources", map[string]any{"kind": "Database", "name": name, "environment": "prod",
				"spec": map[string]any{"engine": engine, "address": u.Host, "host": "db-host", "database": strings.TrimPrefix(u.Path, "/"), "login": "vault://acme/" + login}}, nil)
			t.Run(engine+"/"+u.Host, func(t *testing.T) {
				c.t = t
				switch engine {
				case "mongodb":
					testMongo(t, c, name, strings.TrimPrefix(u.Path, "/"))
				case "redis":
					testRedis(t, c, name, strings.TrimPrefix(u.Path, "/"))
				default:
					testDatabase(t, c, engine, name, strings.TrimPrefix(u.Path, "/"))
				}
			})
		}
	}
}

type dbAnswer struct {
	Risk      string
	Reasons   []string
	Error     string
	Code      string
	Atomic    bool
	Committed bool
	Results   []struct {
		Columns      []struct{ Name string }
		Rows         [][]any
		Truncated    bool
		RowsAffected int64
		Error        string
		Note         string
	}
}

// dbCommon checks info and databases, and returns a function that runs a script.
func dbCommon(t *testing.T, c *client, name, database string, atomic bool) func(script string, write bool, want int) dbAnswer {
	D := "/api/workspaces/acme/databases/" + name
	var info struct {
		Info        map[string]any
		Path        []string
		Environment string
		Protected   bool
		Atomic      bool
	}
	c.must("GET", D+"/info", nil, &info)
	if info.Info["version"] == "" || info.Info["database"] != database || len(info.Path) != 1 || info.Path[0] != "Host/db-host" || !info.Protected || info.Atomic != atomic {
		t.Fatalf("info: %+v", info)
	}
	var dbs []string
	c.must("GET", D+"/databases", nil, &dbs)
	if !contains(dbs, database) {
		t.Fatalf("databases: %v", dbs)
	}
	return func(script string, write bool, want int) dbAnswer {
		t.Helper()
		var a dbAnswer
		if code := c.do("POST", D+"/query", map[string]any{"sql": script, "write": write, "maxRows": 2}, &a); code != want {
			t.Fatalf("%q write=%v: %d, want %d (%+v)", script, write, code, want, a)
		}
		return a
	}
}

func testDatabase(t *testing.T, c *client, engine, name, database string) {
	D := "/api/workspaces/acme/databases/" + name
	run := dbCommon(t, c, name, database, true)
	run("drop table if exists t_opsdeck", true, 200)
	// a change is refused as a read, and runs once confirmed
	if a := run("create table t_opsdeck (id int primary key, name varchar(50))", false, 409); a.Code != "not_read" || a.Risk != "change" {
		t.Fatalf("create as read: %+v", a)
	}
	if a := run("create table t_opsdeck (id int primary key, name varchar(50))", true, 200); !a.Committed || a.Error != "" {
		t.Fatalf("create: %+v", a)
	}
	if a := run("insert into t_opsdeck values (1, 'a'), (2, 'b'), (3, 'c')", true, 200); a.Results[0].RowsAffected != 3 {
		t.Fatalf("insert: %+v", a)
	}
	a := run("select id, name from t_opsdeck order by id", false, 200)
	if r := a.Results[0]; len(r.Columns) != 2 || r.Columns[1].Name != "name" || len(r.Rows) != 2 || !r.Truncated || r.Rows[0][1] != "a" {
		t.Fatalf("select: %+v", a)
	}
	if a := run("begin; insert into t_opsdeck values (5, 'e'); commit", true, 400); !strings.Contains(a.Error, "one transaction") {
		t.Fatalf("transaction control: %+v", a)
	}
	if a := run("select 1; drop table t_opsdeck", false, 409); a.Risk != "destructive" {
		t.Fatalf("drop as read: %+v", a)
	}
	// a failing statement rolls the whole change back
	if a := run("insert into t_opsdeck values (4, 'd'); insert into t_opsdeck values (1, 'dup')", true, 200); a.Committed || a.Error == "" || len(a.Results) != 2 || a.Results[1].Error == "" {
		t.Fatalf("failed change: %+v", a)
	}
	if a := run("select count(*) from t_opsdeck", false, 200); fmt.Sprint(a.Results[0].Rows[0][0]) != "3" {
		t.Fatalf("not rolled back: %+v", a)
	}
	// what the classifier cannot see (a function of the user's that writes)
	// is stopped by the read-only transaction
	fn := "create function opsdeck_w() returns int language sql as $$ insert into t_opsdeck values (9, 'x') returning id $$"
	if engine == "mysql" {
		fn = "create function opsdeck_w() returns int deterministic modifies sql data begin insert into t_opsdeck values (9, 'x'); return 1; end"
	}
	run("drop function if exists opsdeck_w", true, 200)
	run(fn, true, 200)
	if a := run("select opsdeck_w()", false, 200); a.Error == "" || !strings.Contains(strings.ToLower(a.Error), "read") {
		t.Fatalf("write in a read: %+v", a)
	}
	if a := run("select count(*) from t_opsdeck", false, 200); fmt.Sprint(a.Results[0].Rows[0][0]) != "3" {
		t.Fatalf("a read wrote: %+v", a)
	}
	// catalog
	var tables []map[string]any
	c.must("GET", D+"/tables", nil, &tables)
	schema := ""
	for _, tb := range tables {
		if tb["name"] == "t_opsdeck" {
			schema = tb["schema"].(string)
		}
	}
	if schema == "" {
		t.Fatalf("tables: %v", tables)
	}
	var ti struct {
		Columns []struct {
			Name    string
			Primary bool
		}
		Indexes []struct{ Primary bool }
	}
	c.must("GET", D+"/table?schema="+url.QueryEscape(schema)+"&name=t_opsdeck", nil, &ti)
	if len(ti.Columns) != 2 || !ti.Columns[0].Primary || len(ti.Indexes) == 0 || !ti.Indexes[0].Primary {
		t.Fatalf("table: %+v", ti)
	}
	run("drop function opsdeck_w", true, 200)

	// the agent: describe and select run directly, the update waits for approval
	outputs := agentRound(t, c, name, "t_opsdeck", "select id, name from t_opsdeck order by id", "update t_opsdeck set name = 'z' where id = 1")
	if !strings.Contains(outputs[0], "PK") || !strings.Contains(outputs[1], "| a") || !strings.Contains(outputs[1], "(3 rows)") ||
		!strings.Contains(outputs[2], "1 row(s) affected") || !strings.Contains(outputs[2], "committed") {
		t.Fatalf("agent results:\n%s", strings.Join(outputs, "\n----\n"))
	}
	if a := run("select name from t_opsdeck where id = 1", false, 200); a.Results[0].Rows[0][0] != "z" {
		t.Fatalf("approved update not applied: %+v", a)
	}
	run("drop table t_opsdeck", true, 200)
}

func testMongo(t *testing.T, c *client, name, database string) {
	D := "/api/workspaces/acme/databases/" + name
	run := dbCommon(t, c, name, database, false)
	run("db.t_opsdeck.drop()", true, 200)
	if a := run(`db.t_opsdeck.insertMany([{_id: 1, name: "a", tags: ["x"]}, {_id: 2, name: "b"}, {_id: 3, name: "c", nested: {k: 1}}])`, false, 409); a.Code != "not_read" || a.Atomic {
		t.Fatalf("insert as read: %+v", a)
	}
	if a := run(`db.t_opsdeck.insertMany([{_id: 1, name: "a", tags: ["x"]}, {_id: 2, name: "b"}, {_id: 3, name: "c", nested: {k: 1}}])`, true, 200); a.Results[0].RowsAffected != 3 {
		t.Fatalf("insert: %+v", a)
	}
	a := run(`db.t_opsdeck.find().sort({_id: 1})`, false, 200)
	if r := a.Results[0]; len(r.Rows) != 2 || !r.Truncated || r.Columns[0].Name != "_id" || r.Rows[0][1] != "a" {
		t.Fatalf("find: %+v", a)
	}
	// statement by statement: the first insert stays when the second fails
	if a := run(`db.t_opsdeck.insertOne({_id: 4})
		db.t_opsdeck.insertOne({_id: 1})`, true, 200); a.Error == "" || a.Committed || len(a.Results) != 2 {
		t.Fatalf("failing change: %+v", a)
	}
	if a := run(`db.t_opsdeck.countDocuments()`, false, 200); fmt.Sprint(a.Results[0].Rows[0][0]) != "4" {
		t.Fatalf("count: %+v", a)
	}
	if a := run(`db.t_opsdeck.deleteMany({})`, false, 409); a.Risk != "destructive" {
		t.Fatalf("deleteMany as read: %+v", a)
	}
	var tables []map[string]any
	c.must("GET", D+"/tables", nil, &tables)
	found := false
	for _, tb := range tables {
		found = found || (tb["name"] == "t_opsdeck" && tb["query"] == "db.t_opsdeck.find({}).limit(100)")
	}
	if !found {
		t.Fatalf("tables: %v", tables)
	}
	outputs := agentRound(t, c, name, "t_opsdeck", `db.t_opsdeck.find({}, {name: 1}).sort({_id: 1})`, `db.t_opsdeck.updateOne({_id: 1}, {$set: {name: "z"}})`)
	if !strings.Contains(outputs[0], "_id") || !strings.Contains(outputs[0], "fields of the 4") || !strings.Contains(outputs[1], "| a") ||
		!strings.Contains(outputs[2], "matched 1, modified 1") || strings.Contains(outputs[2], "committed") {
		t.Fatalf("agent results:\n%s", strings.Join(outputs, "\n----\n"))
	}
	if a := run(`db.t_opsdeck.findOne({_id: 1})`, false, 200); a.Results[0].Rows[0][1] != "z" {
		t.Fatalf("approved update not applied: %+v", a)
	}
	run("db.t_opsdeck.drop()", true, 200)
}

func testRedis(t *testing.T, c *client, name, database string) {
	D := "/api/workspaces/acme/databases/" + name
	run := dbCommon(t, c, name, database, false)
	run("DEL t_opsdeck:1 t_opsdeck:2", true, 200)
	if a := run("HSET t_opsdeck:1 name a born 1815", false, 409); a.Code != "not_read" {
		t.Fatalf("hset as read: %+v", a)
	}
	run("HSET t_opsdeck:1 name a born 1815\nSET t_opsdeck:2 \"two words\"", true, 200)
	if a := run("HGETALL t_opsdeck:1\nGET t_opsdeck:2", false, 200); len(a.Results) != 2 || a.Results[0].Rows[0][1] != "a" || a.Results[1].Rows[0][0] != "two words" {
		t.Fatalf("read: %+v", a)
	}
	if a := run("MULTI", true, 400); !strings.Contains(a.Error, "transactions") {
		t.Fatalf("multi: %+v", a)
	}
	if a := run("FLUSHALL", false, 409); a.Risk != "destructive" {
		t.Fatalf("flushall as read: %+v", a)
	}
	var tables []map[string]any
	c.must("GET", D+"/tables?match="+url.QueryEscape("t_opsdeck:*"), nil, &tables)
	if len(tables) != 2 || tables[0]["query"] != "HGETALL t_opsdeck:1" || tables[0]["schema"] != "t_opsdeck" {
		t.Fatalf("tables: %v", tables)
	}
	outputs := agentRound(t, c, name, "t_opsdeck:1", "HGET t_opsdeck:1 name", "HSET t_opsdeck:1 name z")
	if !strings.Contains(outputs[0], "hash") || !strings.Contains(outputs[1], "a") || !strings.Contains(outputs[2], "0") {
		t.Fatalf("agent results:\n%s", strings.Join(outputs, "\n----\n"))
	}
	if a := run("HGET t_opsdeck:1 name", false, 200); a.Results[0].Rows[0][0] != "z" {
		t.Fatalf("approved change not applied: %+v", a)
	}
	run("DEL t_opsdeck:1 t_opsdeck:2", true, 200)
}

// agentRound has the agent describe table, run read, then change (which
// must wait for approval); it returns the three tool outputs.
func agentRound(t *testing.T, c *client, name, table, read, change string) []string {
	t.Helper()
	fake := fakellm.New([]fakellm.Turn{
		{Text: "Looking at the table.", Tools: []fakellm.ToolCall{
			{Name: "database", Args: map[string]any{"database": name, "action": "describe", "table": table}},
			{Name: "database", Args: map[string]any{"database": name, "action": "query", "script": read}},
		}},
		{Tools: []fakellm.ToolCall{{Name: "database", Args: map[string]any{"database": name, "action": "query", "sql": change}}}},
		{Text: "Done."},
	})
	defer fake.Close()
	var settings map[string]any
	c.must("GET", "/api/settings", nil, &settings)
	settings["providers"] = []map[string]any{{"id": "fake", "type": "openai", "name": "Fake", "baseUrl": fake.URL, "models": []string{"m1"}}}
	c.must("PUT", "/api/settings", settings, nil)
	var sess map[string]any
	c.must("POST", "/api/workspaces/acme/sessions", map[string]any{"mode": "ask", "model": "fake/m1"}, &sess)
	sid := sess["id"].(string)
	c.must("POST", "/api/workspaces/acme/sessions/"+sid+"/messages", map[string]string{"text": "rename the first row"}, nil)
	var full struct {
		Session map[string]any
		Events  []map[string]any
	}
	approvals := 0
	for i := 0; i < 300; i++ {
		c.must("GET", "/api/workspaces/acme/sessions/"+sid, nil, &full)
		for _, ev := range full.Events {
			if ev["type"] == "approval_request" && approvals == 0 {
				approvals++
				if !strings.Contains(fmt.Sprint(ev), change) {
					t.Fatalf("approval for the wrong call: %v", ev)
				}
				c.must("POST", "/api/workspaces/acme/sessions/"+sid+"/approvals/"+ev["approvalId"].(string), map[string]string{"decision": "approve"}, nil)
			}
		}
		if full.Session["state"] == "idle" && approvals > 0 && len(full.Events) > 6 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	var outputs []string
	for _, ev := range full.Events {
		if ev["type"] == "approval_request" && !strings.Contains(fmt.Sprint(ev), change) {
			t.Errorf("a read asked for approval: %v", ev)
		}
		if ev["type"] == "tool_result" {
			outputs = append(outputs, fmt.Sprint(ev["output"]))
		}
	}
	if len(outputs) != 3 || approvals != 1 {
		t.Fatalf("agent: %d approvals, outputs:\n%s", approvals, strings.Join(outputs, "\n----\n"))
	}
	return outputs
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
