package server

import (
	"context"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"opsdeck/internal/agent"
	"opsdeck/internal/paths"
	"opsdeck/internal/sysproc"
)

type bearerRT struct{ token string }

func (b bearerRT) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.token)
	return http.DefaultTransport.RoundTrip(r)
}

func connectExt(t *testing.T, url, token string) (*mcp.ClientSession, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	client := mcp.NewClient(&mcp.Implementation{Name: "test-agent", Version: "1"}, nil)
	return client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: url, HTTPClient: &http.Client{Transport: bearerRT{token}}}, nil)
}

func textOf(r *mcp.CallToolResult) string {
	var b strings.Builder
	for _, c := range r.Content {
		if t, ok := c.(*mcp.TextContent); ok {
			b.WriteString(t.Text)
		}
	}
	return b.String()
}

// An agent outside opsdeck uses a workspace through /mcp/ext with an access
// key: reads run, a change waits for the approval in the key's session.
func TestExternalMCP(t *testing.T) {
	t.Setenv("OPSDECK_HOME", t.TempDir())
	notified := make(chan string, 4)
	desktopNotify = func(title, body string) error { notified <- title + "\n" + body; return nil }
	defer func() { desktopNotify = sysproc.Notify }()
	srv, err := New(paths.Default(), Options{Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	jar, _ := cookiejar.New(nil)
	c := &client{t: t, base: ts.URL, http: &http.Client{Jar: jar, Timeout: 60 * time.Second}}
	c.must("POST", "/api/auth/login", map[string]string{"token": srv.token}, nil)
	c.must("POST", "/api/vault/init", map[string]string{"password": "correct horse battery"}, nil)
	c.must("POST", "/api/workspaces", map[string]string{"name": "acme", "title": "ACME"}, nil)
	c.must("PUT", "/api/workspaces/acme", map[string]any{"title": "ACME", "environments": []map[string]any{{"name": "prod", "protected": true}}}, nil)

	if code := c.do("POST", "/api/access-keys", map[string]any{"label": "x", "workspace": "nope", "mode": "ask"}, nil); code < 400 {
		t.Fatalf("a key for a missing workspace was made: %d", code)
	}
	var made struct {
		Key    struct{ ID, Hint string }
		Secret string
	}
	c.must("POST", "/api/access-keys", map[string]any{"label": "Claude Code", "workspace": "acme", "mode": "ask"}, &made)
	if !strings.HasPrefix(made.Secret, "odk_") {
		t.Fatalf("secret %q", made.Secret)
	}
	var listed struct{ Keys []map[string]any }
	c.must("GET", "/api/access-keys", nil, &listed)
	if len(listed.Keys) != 1 || listed.Keys[0]["hash"] != nil {
		t.Fatalf("listing: %v", listed.Keys)
	}

	url := ts.URL + "/mcp/ext"
	// a wrong key and a browser are refused
	if _, err := connectExt(t, url, "odk_wrong"); err == nil {
		t.Fatal("a wrong key connected")
	}
	req, _ := http.NewRequest("POST", url, strings.NewReader("{}"))
	req.Header.Set("Authorization", "Bearer "+made.Secret)
	req.Header.Set("Origin", "https://evil.example")
	if res, _ := http.DefaultClient.Do(req); res.StatusCode != 403 {
		t.Fatalf("a request with Origin got %d", res.StatusCode)
	}

	sess, err := connectExt(t, url, made.Secret)
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	if !strings.Contains(sess.InitializeResult().Instructions, `"acme"`) {
		t.Fatalf("instructions: %q", sess.InitializeResult().Instructions)
	}
	ctx := context.Background()
	tools, err := sess.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, tl := range tools.Tools {
		names[tl.Name] = true
	}
	if !names["inventory"] || !names["write_doc"] {
		t.Fatalf("tools: %v", names)
	}
	res, err := sess.CallTool(ctx, &mcp.CallToolParams{Name: "inventory", Arguments: map[string]any{}})
	if err != nil || res.IsError {
		t.Fatalf("inventory: %v %s", err, textOf(res))
	}

	// a change waits for the user's approval in the key's session
	sid := "mcp-" + made.Key.ID
	type out struct {
		res *mcp.CallToolResult
		err error
	}
	done := make(chan out, 1)
	go func() {
		r, err := sess.CallTool(ctx, &mcp.CallToolParams{Name: "write_doc", Arguments: map[string]any{"path": "docs/from-claude.md", "content": "# Written by an outside agent\n"}})
		done <- out{r, err}
	}()
	var aid string
	for i := 0; i < 100 && aid == ""; i++ {
		var got struct {
			Session map[string]any
			Events  []map[string]any
		}
		c.must("GET", "/api/workspaces/acme/sessions/"+sid, nil, &got)
		for _, e := range got.Events {
			if e["type"] == "approval_request" {
				aid, _ = e["approvalId"].(string)
			}
		}
		if aid == "" {
			time.Sleep(50 * time.Millisecond)
		} else if got.Session["client"] != "Claude Code" || got.Session["state"] != "waiting_approval" {
			t.Fatalf("session: %v", got.Session)
		}
	}
	if aid == "" {
		t.Fatal("no approval request in the client session")
	}
	select {
	case <-done:
		t.Fatal("the change ran before it was approved")
	default:
	}
	// it is listed for the whole app, and the desktop is told (no window has the focus)
	var waiting struct{ Items []map[string]any }
	c.must("GET", "/api/waiting", nil, &waiting)
	if len(waiting.Items) != 1 || waiting.Items[0]["id"] != aid || waiting.Items[0]["session"] != sid || waiting.Items[0]["tool"] != "write_doc" || waiting.Items[0]["client"] != "Claude Code" {
		t.Fatalf("waiting: %v", waiting.Items)
	}
	select {
	case n := <-notified:
		if !strings.Contains(n, "Claude Code waits for your approval") || !strings.Contains(n, "write_doc") {
			t.Fatalf("notification %q", n)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no desktop notification")
	}
	c.must("POST", "/api/workspaces/acme/sessions/"+sid+"/approvals/"+aid, map[string]any{"decision": "approve"}, nil)
	select {
	case o := <-done:
		if o.err != nil || o.res.IsError {
			t.Fatalf("write_doc: %v %s", o.err, textOf(o.res))
		}
	case <-time.After(20 * time.Second):
		t.Fatal("the approved call did not return")
	}
	c.must("GET", "/api/waiting", nil, &waiting)
	if len(waiting.Items) != 0 {
		t.Fatalf("still waiting: %v", waiting.Items)
	}
	ws, _ := srv.ws.Get("acme")
	if b, err := os.ReadFile(filepath.Join(ws.Path, "docs", "from-claude.md")); err != nil || !strings.Contains(string(b), "outside agent") {
		t.Fatalf("doc: %q %v", b, err)
	}

	// nobody chats in a client session
	if code := c.do("POST", "/api/workspaces/acme/sessions/"+sid+"/messages", map[string]any{"text": "hi"}, nil); code < 400 {
		t.Fatalf("a message to a client session was accepted: %d", code)
	}

	// a locked opsdeck answers with a clear error
	c.must("POST", "/api/vault/lock", nil, nil)
	res, err = sess.CallTool(ctx, &mcp.CallToolParams{Name: "inventory", Arguments: map[string]any{}})
	if err != nil || !res.IsError || !strings.Contains(textOf(res), "locked") {
		t.Fatalf("locked: %v %v %s", err, res.IsError, textOf(res))
	}
	c.must("POST", "/api/vault/unlock", map[string]string{"password": "correct horse battery"}, nil)

	// a revoked key stops working at once
	c.must("DELETE", "/api/access-keys/"+made.Key.ID, nil, nil)
	if _, err := sess.CallTool(ctx, &mcp.CallToolParams{Name: "inventory", Arguments: map[string]any{}}); err == nil {
		t.Fatal("a revoked key still works")
	}
}

// Connecting an agent in one click writes the key into its config;
// rotating rewrites it, revoking takes it out again.
func TestConnectAgentConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("OPSDECK_HOME", t.TempDir())
	srv, err := New(paths.Default(), Options{Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	jar, _ := cookiejar.New(nil)
	c := &client{t: t, base: ts.URL, http: &http.Client{Jar: jar, Timeout: 60 * time.Second}}
	c.must("POST", "/api/auth/login", map[string]string{"token": srv.token}, nil)
	c.must("POST", "/api/vault/init", map[string]string{"password": "correct horse battery"}, nil)
	c.must("POST", "/api/workspaces", map[string]string{"name": "acme", "title": "ACME"}, nil)

	type made struct {
		Key       struct{ ID, Client, Server string }
		Secret    string
		Link      map[string]any
		LinkError string
	}
	var m made
	c.must("POST", "/api/access-keys", map[string]any{"label": "Cursor", "workspace": "acme", "mode": "ask", "client": "cursor"}, &m)
	if m.LinkError != "" || m.Key.Client != "cursor" || m.Key.Server != "opsdeck-acme" {
		t.Fatalf("%+v", m)
	}
	cfg := filepath.Join(home, ".cursor", "mcp.json")
	if b, _ := os.ReadFile(cfg); !strings.Contains(string(b), m.Secret) {
		t.Fatalf("config %s", b)
	}
	var listed struct{ Keys []map[string]any }
	c.must("GET", "/api/access-keys", nil, &listed)
	if listed.Keys[0]["linked"] != true {
		t.Fatalf("%v", listed.Keys)
	}

	var r made
	c.must("POST", "/api/access-keys/"+m.Key.ID+"/rotate", map[string]any{}, &r)
	if r.Secret == m.Secret || r.Key.ID != m.Key.ID {
		t.Fatalf("%+v", r)
	}
	if b, _ := os.ReadFile(cfg); !strings.Contains(string(b), r.Secret) || strings.Contains(string(b), m.Secret) {
		t.Fatalf("config after rotate %s", b)
	}

	// connecting the same agent to the same workspace again replaces the key
	var m2 made
	c.must("POST", "/api/access-keys", map[string]any{"label": "Cursor 2", "workspace": "acme", "mode": "auto", "client": "cursor"}, &m2)
	c.must("GET", "/api/access-keys", nil, &listed)
	if len(listed.Keys) != 1 || listed.Keys[0]["id"] != m2.Key.ID {
		t.Fatalf("old key kept: %v", listed.Keys)
	}

	c.must("DELETE", "/api/access-keys/"+m2.Key.ID, nil, nil)
	if b, _ := os.ReadFile(cfg); strings.Contains(string(b), "opsdeck-acme") {
		t.Fatalf("config after revoke %s", b)
	}
}

func TestPresenceAndText(t *testing.T) {
	var p presence
	if p.watched() {
		t.Fatal("watched before any report")
	}
	p.set(true)
	if !p.watched() {
		t.Fatal("focused window not watched")
	}
	p.at = time.Now().Add(-2 * time.Minute)
	if p.watched() {
		t.Fatal("a stale report counts")
	}
	title, body := waitingText(agent.Waiting{Kind: "approval", Tool: "exec", Target: "host/db-1", Environment: "prod", Risk: "destructive", Workspace: "acme", SessionTitle: "Clean up"})
	if title != "The agent waits for your approval ⚠ destructive" || !strings.HasPrefix(body, "exec on host/db-1 (prod)") || !strings.HasSuffix(body, "acme · Clean up") {
		t.Fatalf("%q %q", title, body)
	}
}
