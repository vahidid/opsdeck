package dbx

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"reflect"
	"strings"
	"testing"

	"opsdeck/internal/workspace"
)

func TestSplitRedisLine(t *testing.T) {
	cases := map[string][]string{
		`GET user:1`:              {"GET", "user:1"},
		`SET "a b" "x\ny\x41\"" `: {"SET", "a b", "x\nyA\""},
		`HSET h 'it''s' 'a\'b'`:   nil, // closing quote must be followed by a space
		`HSET h 'a\'b' ""`:        {"HSET", "h", "a'b", ""},
		"  LRANGE  l\t0 -1 ":      {"LRANGE", "l", "0", "-1"},
	}
	for in, want := range cases {
		got, err := splitRedisLine(in)
		if want == nil {
			if err == nil {
				t.Errorf("%q: no error", in)
			}
			continue
		}
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Errorf("%q: %q %v, want %q", in, got, err, want)
		}
	}
	for _, s := range []string{"a b", "x\ny", `q"`, "", "plain:key", "\x00\xff"} {
		got, err := splitRedisLine("GET " + redisQuote(s))
		if err != nil || len(got) != 2 || got[1] != s {
			t.Errorf("quote %q: %q %v", s, got, err)
		}
	}
}

func TestClassifyRedis(t *testing.T) {
	cases := []struct{ script, risk string }{
		{"GET a\nHGETALL h\n# comment\nSCAN 0 MATCH user:* COUNT 100", RiskRead},
		{"info memory\nconfig get maxmemory\nCLIENT LIST\nOBJECT ENCODING k\nSELECT 2", RiskRead},
		{"ZRANGE z 0 -1 WITHSCORES\nXRANGE s - +\nEVAL_RO 'return 1' 0", RiskRead},
		{"SET a 1", RiskChange},
		{"DEL a b", RiskChange},
		{"CONFIG SET maxmemory 1gb", RiskChange},
		{"EVAL 'return 1' 0", RiskChange},
		{"OBJECT FOO k", RiskChange},
		{"GETDEL a", RiskChange},
		{"MULTI", RiskChange},
		{"GET a\nFLUSHALL", RiskDestructive},
		{"flushdb async", RiskDestructive},
		{"SCRIPT FLUSH", RiskDestructive},
		{"SHUTDOWN NOSAVE", RiskDestructive},
		{`GET "unterminated`, RiskChange},
	}
	for _, c := range cases {
		if got := Classify("redis", c.script); got.Risk != c.risk {
			t.Errorf("%q: %s, want %s (%v)", c.script, got.Risk, c.risk, got.Reasons)
		}
	}
	for _, s := range []string{"MULTI", "subscribe ch", "BLPOP q 0", "XREAD BLOCK 0 STREAMS s $", "MONITOR", "AUTH x y"} {
		args, _ := splitRedisLine(s)
		if redisRefusal(args) == "" {
			t.Errorf("%q is not refused", s)
		}
	}
}

func FuzzParseRedis(f *testing.F) {
	f.Add("SET \"a\\x41\" 'b'\nGET a")
	f.Fuzz(func(t *testing.T, s string) {
		Classify("redis", s)
	})
}

// OPSDECK_TEST_REDIS=[user]:pass@host:port[/db] runs this against a real server.
func TestRedisLive(t *testing.T) {
	spec := os.Getenv("OPSDECK_TEST_REDIS")
	if spec == "" {
		t.Skip("OPSDECK_TEST_REDIS not set")
	}
	cred, addr, _ := strings.Cut(spec, "@")
	addr, _, _ = strings.Cut(addr, "/") // the database is the test's own
	user, pass, _ := strings.Cut(cred, ":")
	ctx := context.Background()
	res := &workspace.Resource{Kind: "Database", Name: "r", Spec: map[string]any{"engine": "redis"}}
	s, err := openRedis(ctx, target{res: res, addr: addr, user: user, password: pass, database: "3", dial: (&net.Dialer{}).DialContext})
	if err != nil {
		t.Fatal(err)
	}
	defer s.close()
	run := func(script string, write bool) []Result {
		t.Helper()
		_, r, err := s.Run(ctx, script, QueryOptions{Write: write})
		if err != nil {
			t.Fatalf("%s: %v (%+v)", script, err, r)
		}
		return r
	}
	run("FLUSHDB", true)
	run(`SET user:1:name "Ada Lovelace"
		HSET user:1 name Ada born 1815
		RPUSH queue:jobs a b c
		ZADD board 10 ada 20 linus
		SADD tags x y
		XADD events * kind login user 1
		SET bin "\x00\xff"`, true)
	if _, _, err := s.Run(ctx, "DEL user:1", QueryOptions{}); err == nil {
		t.Fatal("a change ran as a read")
	}
	if _, _, err := s.Run(ctx, "MULTI", QueryOptions{Write: true}); err == nil {
		t.Fatal("MULTI ran")
	}
	r := run("GET user:1:name\nHGETALL user:1\nLRANGE queue:jobs 0 -1\nZRANGE board 0 -1 WITHSCORES\nGET missing\nXRANGE events - +\nGET bin\nINFO keyspace\nSCAN 0 MATCH user:*", false)
	if r[0].Rows[0][0] != "Ada Lovelace" || len(r[1].Rows) != 2 || r[1].Rows[1][1] != "1815" || len(r[2].Rows) != 3 ||
		r[3].Columns[0].Name != "member" || r[3].Rows[1][1] != "20" || r[4].Rows[0][0] != nil {
		t.Fatalf("results: %+v", r)
	}
	if raw, _ := r[5].Rows[0][1].(json.RawMessage); string(raw) != `{"kind":"login","user":"1"}` {
		t.Fatalf("xrange: %#v", r[5].Rows)
	}
	if r[6].Rows[0][0] != `\x00ff` {
		t.Fatalf("binary: %#v", r[6].Rows[0][0])
	}
	if len(r[7].Rows) != 1 || r[7].Rows[0][1] != "db3" || len(r[8].Rows) != 2 {
		t.Fatalf("info/scan: %+v %+v", r[7], r[8])
	}
	// SELECT lasts for the script only
	r = run("SELECT 0\nDBSIZE", false)
	if r2 := run("DBSIZE", false); r2[0].Rows[0][0] != int64(7) {
		t.Fatalf("SELECT leaked: %+v", r2)
	}
	ts, err := s.Tables(ctx, "user:*")
	if err != nil || len(ts) != 2 || ts[0].Name != "user:1" || ts[0].Type != "hash" || ts[0].Rows != 2 || ts[0].Schema != "user" || ts[0].Query != "HGETALL user:1" {
		t.Fatalf("tables: %+v %v", ts, err)
	}
	ti, err := s.Table(ctx, "", "queue:jobs")
	if err != nil || ti.Properties[0].Value != "list" {
		t.Fatalf("table: %+v %v", ti, err)
	}
	if dbs, err := s.Databases(ctx); err != nil || len(dbs) < 4 {
		t.Fatalf("databases: %v %v", dbs, err)
	}
	if in, err := s.Info(ctx); err != nil || !strings.HasPrefix(in.Version, "Redis ") {
		t.Fatalf("info: %+v %v", in, err)
	}
}

// Requests that open the same database at once share one session: none
// closes another's.
func TestConnConcurrent(t *testing.T) {
	spec := os.Getenv("OPSDECK_TEST_REDIS")
	if spec == "" {
		t.Skip("OPSDECK_TEST_REDIS not set")
	}
	cred, addr, _ := strings.Cut(spec, "@")
	addr, _, _ = strings.Cut(addr, "/")
	if strings.Trim(cred, ":") != "" {
		t.Skip("needs a server without a password")
	}
	m := &Manager{conns: map[string]cached{}}
	w := &workspace.Workspace{Metadata: workspace.Meta{Name: "acme"}}
	w.Resources = []*workspace.Resource{{Kind: "Database", Name: "r", Spec: map[string]any{"engine": "redis", "address": addr}}}
	res := w.Resources[0]
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		go func() {
			s, err := m.Conn(context.Background(), w, res, "")
			if err == nil {
				_, err = s.Info(context.Background())
			}
			errs <- err
		}()
	}
	for i := 0; i < 8; i++ {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
}
