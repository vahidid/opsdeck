package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"opsdeck/internal/connect"
	"opsdeck/internal/vault"
	"opsdeck/internal/workspace"
)

// apiEnv is an Env with a vault holding a token and a login, and a fake
// server answering like Grafana, Prometheus, Loki and Elasticsearch.
func apiEnv(t *testing.T) (*Env, *fakeAPI) {
	t.Helper()
	dir := t.TempDir()
	v, err := vault.Open(filepath.Join(dir, "vault.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { v.Close() })
	if err := v.Init("correct horse battery"); err != nil {
		t.Fatal(err)
	}
	v.EnsureScope("t")
	for _, it := range []vault.Item{
		{Scope: "t", Type: "token", Name: "grafana-token", Fields: []vault.Field{{Name: "token", Value: "glsa_secret123", Secret: true}}, Policy: vault.Policy{AgentUse: true}},
		{Scope: "t", Type: "login", Name: "prom", Fields: []vault.Field{{Name: "username", Value: "ops"}, {Name: "password", Value: "pr0m-pass", Secret: true}}, Policy: vault.Policy{AgentUse: true}},
		{Scope: "t", Type: "token", Name: "es-key", Fields: []vault.Field{{Name: "token", Value: "ZXMta2V5", Secret: true}}, Policy: vault.Policy{AgentUse: true}},
	} {
		if _, err := v.Create(it); err != nil {
			t.Fatal(err)
		}
	}
	f := &fakeAPI{}
	f.srv = httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(f.srv.Close)
	w := &workspace.Workspace{Metadata: workspace.Meta{Name: "t"}, Path: dir, Resources: []*workspace.Resource{
		{Kind: "Panel", Name: "grafana", Environment: "prod", Spec: map[string]any{"url": f.srv.URL + "/grafana", "token": "vault://t/grafana-token"}},
		{Kind: "Prometheus", Name: "prom", Spec: map[string]any{"url": f.srv.URL, "login": "vault://t/prom"}},
		{Kind: "Loki", Name: "loki", Spec: map[string]any{"url": f.srv.URL, "tenant": "acme"}},
		{Kind: "Elasticsearch", Name: "es", Spec: map[string]any{"url": f.srv.URL, "token": "vault://t/es-key", "index": "logs-*"}},
	}}
	w.Environments = []workspace.Environment{{Name: "prod", Protected: true}}
	return &Env{WS: w, Vault: v, Hub: connect.NewHub(v, func(string) string { return dir })}, f
}

type fakeAPI struct {
	srv  *httptest.Server
	mu   sync.Mutex
	seen []*http.Request
	body map[string]string
}

func (f *fakeAPI) handle(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	f.seen = append(f.seen, r)
	if f.body == nil {
		f.body = map[string]string{}
	}
	f.body[r.URL.Path] = string(b)
	f.mu.Unlock()
	now := float64(time.Now().Unix())
	switch r.URL.Path {
	case "/grafana/api/health":
		if r.Header.Get("Authorization") != "Bearer glsa_secret123" {
			http.Error(w, "unauthorized", 401)
			return
		}
		fmt.Fprint(w, `{"database":"ok","version":"11.2.0"}`)
	case "/grafana/api/dashboards/uid/x":
		w.WriteHeader(200)
		fmt.Fprint(w, `{"status":"deleted"}`)
	case "/grafana/login":
		http.Redirect(w, r, "https://evil.example/steal", 302)
	case "/api/v1/query":
		if u, p, _ := r.BasicAuth(); u != "ops" || p != "pr0m-pass" {
			http.Error(w, "unauthorized", 401)
			return
		}
		fmt.Fprintf(w, `{"status":"success","data":{"resultType":"vector","result":[
			{"metric":{"__name__":"up","instance":"a:9100"},"value":[%v,"1"]},
			{"metric":{"__name__":"up","instance":"b:9100"},"value":[%v,"0"]},
			{"metric":{"__name__":"up","instance":"c:9100"},"value":[%v,"5"]}]}}`, now, now, now)
	case "/api/v1/query_range":
		var pts []string
		for i := 0; i < 300; i++ {
			pts = append(pts, fmt.Sprintf(`[%v,"%d"]`, now-float64(300-i)*15, i%50))
		}
		fmt.Fprintf(w, `{"status":"success","data":{"resultType":"matrix","result":[{"metric":{"instance":"a"},"values":[%s]}]}}`, strings.Join(pts, ","))
	case "/api/v1/alerts":
		fmt.Fprint(w, `{"status":"success","data":{"alerts":[{"labels":{"alertname":"DiskFull","severity":"critical","instance":"db-1"},"annotations":{"summary":"disk 95% full"},"state":"firing","activeAt":"2026-10-24T08:00:00Z"}]}}`)
	case "/loki/api/v1/query_range":
		if r.Header.Get("X-Scope-OrgID") != "acme" {
			http.Error(w, "no org id", 401)
			return
		}
		n := time.Now().UnixNano()
		fmt.Fprintf(w, `{"status":"success","data":{"resultType":"streams","result":[
			{"stream":{"app":"shop"},"values":[["%d","level=error upstream timed out"],["%d","GET / 200"]]},
			{"stream":{"app":"api"},"values":[["%d","warn: slow query"]]}]}}`, n-1e9, n-5e9, n-2e9)
	case "/logs-*/_search":
		if r.Header.Get("Authorization") != "ApiKey ZXMta2V5" {
			http.Error(w, "unauthorized", 401)
			return
		}
		fmt.Fprint(w, `{"hits":{"hits":[{"_index":"logs-2026","_source":{"@timestamp":"2026-10-24T10:00:00Z","message":"connection refused","log":{"level":"ERROR"},"service":{"name":"shop"}}}]}}`)
	default:
		http.NotFound(w, r)
	}
}

func prepare(t *testing.T, tl Tool, env *Env, in map[string]any) *Call {
	t.Helper()
	b, _ := json.Marshal(in)
	c, err := tl.Prepare(context.Background(), env, b)
	if err != nil {
		t.Fatalf("%s %v: %v", tl.Def.Name, in, err)
	}
	return c
}

func viewOf(t *testing.T, c *Call) (string, any) {
	t.Helper()
	sc := &callScope{}
	out, err := c.Run(withCallScope(context.Background(), sc))
	if err != nil {
		t.Fatalf("run: %v\n%s", err, out)
	}
	return out, sc.view
}

func TestHTTPRequest(t *testing.T) {
	env, f := apiEnv(t)
	tl := toolHTTPRequest()
	c := prepare(t, tl, env, map[string]any{"resource": "grafana", "path": "/api/health"})
	if c.Risk != RiskRead || c.Target != "panel/grafana" || !c.Protected || c.Environment != "prod" {
		t.Fatalf("call: %+v", c)
	}
	if !strings.Contains(strings.Join(c.Credentials, ","), "vault://t/grafana-token") {
		t.Fatalf("credentials: %v", c.Credentials)
	}
	out, view := viewOf(t, c)
	if !strings.Contains(out, "HTTP 200 OK") || !strings.Contains(out, `"version": "11.2.0"`) || strings.Contains(out, "glsa_secret123") {
		t.Fatalf("output: %s", out)
	}
	if v := view.(map[string]any); v["kind"] != "http" || v["status"] != 200 {
		t.Fatalf("view: %v", view)
	}
	for method, risk := range map[string]string{"POST": RiskChange, "PUT": RiskChange, "PATCH": RiskChange, "DELETE": RiskDestructive, "HEAD": RiskRead} {
		if c := prepare(t, tl, env, map[string]any{"resource": "grafana", "method": method, "path": "/api/dashboards/uid/x"}); c.Risk != risk || c.Match != method+" /grafana/api/dashboards/uid/x" {
			t.Errorf("%s: %s %q", method, c.Risk, c.Match)
		}
	}
	// redirects are returned, not followed (credentials stay with the panel)
	out, _ = viewOf(t, prepare(t, tl, env, map[string]any{"resource": "grafana", "path": "/login"}))
	if !strings.Contains(out, "302") || !strings.Contains(out, "Location: https://evil.example/steal") {
		t.Fatalf("redirect: %s", out)
	}
	for _, bad := range []map[string]any{
		{"resource": "grafana", "path": "https://evil.example/x"},
		{"resource": "grafana", "path": "//evil.example/x"},
		{"resource": "grafana", "path": "/x", "headers": map[string]any{"Authorization": "Bearer mine"}},
		{"resource": "nope", "path": "/x"},
	} {
		b, _ := json.Marshal(bad)
		if _, err := tl.Prepare(context.Background(), env, b); err == nil {
			t.Errorf("accepted %v", bad)
		}
	}
	_ = f
}

func TestMetricsQuery(t *testing.T) {
	env, _ := apiEnv(t)
	tl := toolMetricsQuery()
	out, view := viewOf(t, prepare(t, tl, env, map[string]any{"query": "up"}))
	if !strings.Contains(out, "3 series (highest first):\nup{instance=\"c:9100\"} 5\nup{instance=\"a:9100\"} 1\nup{instance=\"b:9100\"} 0") {
		t.Fatalf("instant: %s", out)
	}
	if view.(map[string]any)["kind"] != "vector" {
		t.Fatalf("view: %v", view)
	}
	out, view = viewOf(t, prepare(t, tl, env, map[string]any{"query": "rate(x[5m])", "since": "75m"}))
	if !strings.Contains(out, `{instance="a"} min=0 avg=24.5 max=49 last=49`) {
		t.Fatalf("range: %s", out)
	}
	v := view.(map[string]any)
	pts := v["series"].([]map[string]any)[0]["points"].([][2]float64)
	if v["kind"] != "timeseries" || len(pts) != 160 {
		t.Fatalf("range view: %v points", len(pts))
	}
	out, _ = viewOf(t, prepare(t, tl, env, map[string]any{"action": "alerts"}))
	if !strings.Contains(out, "DiskFull [firing]") || !strings.Contains(out, "disk 95% full") {
		t.Fatalf("alerts: %s", out)
	}
}

func TestLogsQuery(t *testing.T) {
	env, f := apiEnv(t)
	tl := toolLogsQuery()
	out, view := viewOf(t, prepare(t, tl, env, map[string]any{"source": "loki", "query": `{app=~".+"}`, "since": "10m"}))
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if !strings.HasPrefix(lines[0], "3 lines, newest first; 1 error, 1 warn") || !strings.Contains(lines[2], "upstream timed out") || !strings.Contains(lines[4], "slow query") {
		t.Fatalf("loki: %s", out)
	}
	if ll := view.(map[string]any)["lines"].([]logLine); ll[0].Level != "error" {
		t.Fatalf("level: %+v", ll[0])
	}
	out, _ = viewOf(t, prepare(t, tl, env, map[string]any{"source": "es", "query": "service.name:shop AND connection"}))
	if !strings.Contains(out, "connection refused") || !strings.Contains(out, `service.name="shop"`) {
		t.Fatalf("es: %s", out)
	}
	f.mu.Lock()
	body := f.body["/logs-*/_search"]
	f.mu.Unlock()
	if !strings.Contains(body, `"query_string":{"default_operator":"AND","query":"service.name:shop AND connection"}`) || !strings.Contains(body, `"@timestamp"`) {
		t.Fatalf("es body: %s", body)
	}
}

func TestSoftwareLookup(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/ubuntu.json":
			fmt.Fprint(w, `[{"cycle":"24.04","releaseDate":"2024-04-25","eol":"2029-05-31","latest":"24.04.1","lts":true,"support":"2029-05-31"},
				{"cycle":"20.04","releaseDate":"2020-04-23","eol":"2025-05-31","latest":"20.04.6","lts":true,"support":"2022-10-01"}]`)
		case "/v1/query":
			b, _ := io.ReadAll(r.Body)
			if !strings.Contains(string(b), `"ecosystem":"Debian:12"`) {
				http.Error(w, string(b), 400)
				return
			}
			fmt.Fprint(w, `{"vulns":[{"id":"DSA-1","aliases":["CVE-2026-1"],"summary":"heap overflow","published":"2026-01-01","affected":[{"ranges":[{"events":[{"introduced":"0"},{"fixed":"3.0.15-1"}]}]}]}]}`)
		case "/repos/traefik/traefik/releases":
			fmt.Fprint(w, `[{"tag_name":"v3.2.0","name":"v3.2.0","published_at":"2026-10-01T00:00:00Z","body":"new things"},{"tag_name":"v3.1.0","published_at":"2026-08-01T00:00:00Z","body":"older"},{"tag_name":"v3.0.0"}]`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	ends := map[string]string{"endoflife": srv.URL, "osv": srv.URL, "github": srv.URL}
	env := &Env{Web: &webClient{cfg: WebConfig{Endpoints: ends, AllowPrivate: true}}, WebLevel: WebFetch}
	tl := toolSoftwareLookup()
	out, _ := viewOf(t, prepare(t, tl, env, map[string]any{"action": "eol", "product": "ubuntu", "version": "20.04.6"}))
	if !strings.Contains(out, "- 20.04: released 2020-04-23, latest 20.04.6; support ended since 2022-10-01; end of life since 2025-05-31 (LTS)   ← 20.04.6") {
		t.Fatalf("eol: %s", out)
	}
	out, _ = viewOf(t, prepare(t, tl, env, map[string]any{"action": "vulns", "ecosystem": "Debian:12", "package": "openssl", "version": "3.0.11-1"}))
	if !strings.Contains(out, "DSA-1 CVE-2026-1: heap overflow — fixed in 3.0.15-1") {
		t.Fatalf("vulns: %s", out)
	}
	out, _ = viewOf(t, prepare(t, tl, env, map[string]any{"action": "releases", "repo": "https://github.com/traefik/traefik", "since": "v3.1.0"}))
	if !strings.Contains(out, "1 releases of traefik/traefik, newest first since v3.1.0") || strings.Contains(out, "older") {
		t.Fatalf("releases: %s", out)
	}
	if _, err := tl.Prepare(context.Background(), &Env{}, json.RawMessage(`{"action":"eol","product":"ubuntu"}`)); err == nil {
		t.Fatal("software_lookup without web access")
	}
}
