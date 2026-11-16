package server

import (
	"context"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"opsdeck/internal/paths"
)

// elicitServer is an opsdeck with workspace acme, served over HTTP.
func elicitServer(t *testing.T) (*Server, *client, string) {
	t.Helper()
	t.Setenv("OPSDECK_HOME", t.TempDir())
	t.Cleanup(setDesktopNotify(func(title, body string) error { return nil }))
	srv, err := New(paths.Default(), Options{Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(srv.Close)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	jar, _ := cookiejar.New(nil)
	c := &client{t: t, base: ts.URL, http: &http.Client{Jar: jar, Timeout: 60 * time.Second}}
	c.must("POST", "/api/auth/login", map[string]string{"token": srv.token}, nil)
	c.must("POST", "/api/vault/init", map[string]string{"password": "correct horse battery"}, nil)
	c.must("POST", "/api/workspaces", map[string]string{"name": "acme", "title": "ACME"}, nil)
	return srv, c, ts.URL + "/mcp/ext"
}

func newKey(c *client, label, mode string) (id, secret string) {
	var made struct {
		Key    struct{ ID string }
		Secret string
	}
	c.must("POST", "/api/access-keys", map[string]any{"label": label, "workspace": "acme", "mode": mode}, &made)
	return made.Key.ID, made.Secret
}

// connectAnswering connects a client that can show a form; answer plays
// its user.
func connectAnswering(t *testing.T, url, token string, answer func(*mcp.ElicitRequest) *mcp.ElicitResult) *mcp.ClientSession {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	client := mcp.NewClient(&mcp.Implementation{Name: "test-agent", Version: "1"}, &mcp.ClientOptions{
		ElicitationHandler: func(ctx context.Context, req *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
			return answer(req), nil
		},
	})
	sess, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: url, HTTPClient: &http.Client{Transport: bearerRT{token}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sess.Close() })
	return sess
}

func writeDoc(sess *mcp.ClientSession, name string) (*mcp.CallToolResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return sess.CallTool(ctx, &mcp.CallToolParams{Name: "write_doc", Arguments: map[string]any{"path": "docs/" + name, "content": "# from the harness\n"}})
}

// resolvedEvent returns the approval_resolved event of the key's session.
func resolvedEvents(c *client, keyID string) []map[string]any {
	var got struct{ Events []map[string]any }
	c.must("GET", "/api/workspaces/acme/sessions/mcp-"+keyID, nil, &got)
	var out []map[string]any
	for _, e := range got.Events {
		if e["type"] == "approval_resolved" {
			out = append(out, e)
		}
	}
	return out
}

// A client that can show a form gets the approval and its answer applies:
// approve runs the change, deny refuses it with the note.
func TestHarnessApprovesThroughElicitation(t *testing.T) {
	srv, c, url := elicitServer(t)
	keyID, secret := newKey(c, "Claude Code", "ask")
	var asked atomic.Value
	answer := atomic.Value{}
	answer.Store(&mcp.ElicitResult{Action: "accept", Content: map[string]any{"decision": "approve"}})
	sess := connectAnswering(t, url, secret, func(req *mcp.ElicitRequest) *mcp.ElicitResult {
		asked.Store(req.Params.Message)
		return answer.Load().(*mcp.ElicitResult)
	})

	res, err := writeDoc(sess, "approved.md")
	if err != nil || res.IsError {
		t.Fatalf("approved write_doc: %v %s", err, textOf(res))
	}
	msg, _ := asked.Load().(string)
	if !strings.Contains(msg, "write_doc") || !strings.Contains(msg, "opsdeck app") {
		t.Fatalf("message: %q", msg)
	}
	ws, _ := srv.ws.Get("acme")
	if _, err := os.Stat(filepath.Join(ws.Path, "docs", "approved.md")); err != nil {
		t.Fatal("the approved change did not run")
	}
	ev := resolvedEvents(c, keyID)
	if len(ev) != 1 || ev[0]["decision"] != "approve" || ev[0]["by"] != "Claude Code" {
		t.Fatalf("resolved: %v", ev)
	}

	answer.Store(&mcp.ElicitResult{Action: "accept", Content: map[string]any{"decision": "deny", "note": "not on a Friday"}})
	res, err = writeDoc(sess, "denied.md")
	if err != nil || !res.IsError || !strings.Contains(textOf(res), "not on a Friday") {
		t.Fatalf("denied write_doc: %v %v %s", err, res.IsError, textOf(res))
	}
	if _, err := os.Stat(filepath.Join(ws.Path, "docs", "denied.md")); err == nil {
		t.Fatal("a denied change ran")
	}
	if ev := resolvedEvents(c, keyID); len(ev) != 2 || ev[1]["decision"] != "deny" || ev[1]["by"] != "Claude Code" {
		t.Fatalf("resolved: %v", ev)
	}
}

// What the mode refuses is never sent to the client: the harness cannot
// approve it.
func TestHarnessCannotApproveWhatTheModeDenies(t *testing.T) {
	_, c, url := elicitServer(t)
	_, secret := newKey(c, "Codex", "plan")
	var asked atomic.Int32
	sess := connectAnswering(t, url, secret, func(*mcp.ElicitRequest) *mcp.ElicitResult {
		asked.Add(1)
		return &mcp.ElicitResult{Action: "accept", Content: map[string]any{"decision": "approve"}}
	})
	res, err := writeDoc(sess, "plan.md")
	if err != nil || !res.IsError || !strings.Contains(textOf(res), "Denied") {
		t.Fatalf("write_doc in plan mode: %v %s", err, textOf(res))
	}
	if asked.Load() != 0 {
		t.Fatal("the client was asked to approve what the mode denies")
	}
}

// Dismissing the form (cancel) leaves the approval to the app; a client
// without elicitation keeps today's behaviour: the approval waits in the app.
func TestHarnessWithoutAnswerLeavesApprovalToTheApp(t *testing.T) {
	for _, tc := range []struct {
		name    string
		connect func(t *testing.T, url, secret string) *mcp.ClientSession
	}{
		{"cancelled", func(t *testing.T, url, secret string) *mcp.ClientSession {
			return connectAnswering(t, url, secret, func(*mcp.ElicitRequest) *mcp.ElicitResult { return &mcp.ElicitResult{Action: "cancel"} })
		}},
		// a client that cannot show the form and declines at once (the
		// Claude Code extension in VS Code): not a denial
		{"declined at once", func(t *testing.T, url, secret string) *mcp.ClientSession {
			return connectAnswering(t, url, secret, func(*mcp.ElicitRequest) *mcp.ElicitResult { return &mcp.ElicitResult{Action: "decline"} })
		}},
		{"accepted without a choice", func(t *testing.T, url, secret string) *mcp.ClientSession {
			return connectAnswering(t, url, secret, func(*mcp.ElicitRequest) *mcp.ElicitResult {
				return &mcp.ElicitResult{Action: "accept", Content: map[string]any{}}
			})
		}},
		{"unsupported", func(t *testing.T, url, secret string) *mcp.ClientSession {
			sess, err := connectExt(t, url, secret)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { sess.Close() })
			return sess
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, c, url := elicitServer(t)
			keyID, secret := newKey(c, "Cursor", "ask")
			sess := tc.connect(t, url, secret)
			done := make(chan *mcp.CallToolResult, 1)
			go func() {
				res, _ := writeDoc(sess, "app.md")
				done <- res
			}()
			var aid string
			for i := 0; i < 200 && aid == ""; i++ {
				var got struct{ Events []map[string]any }
				c.must("GET", "/api/workspaces/acme/sessions/mcp-"+keyID, nil, &got)
				for _, e := range got.Events {
					if e["type"] == "approval_request" {
						aid, _ = e["approvalId"].(string)
					}
				}
				time.Sleep(25 * time.Millisecond)
			}
			if aid == "" {
				t.Fatal("no approval in the app")
			}
			select {
			case <-done:
				t.Fatal("the call ended without an answer in the app")
			case <-time.After(300 * time.Millisecond):
			}
			c.must("POST", "/api/workspaces/acme/sessions/mcp-"+keyID+"/approvals/"+aid, map[string]any{"decision": "approve"}, nil)
			select {
			case res := <-done:
				if res == nil || res.IsError {
					t.Fatalf("write_doc: %v", res)
				}
			case <-time.After(20 * time.Second):
				t.Fatal("the approved call did not return")
			}
			if ev := resolvedEvents(c, keyID); len(ev) != 1 || ev[0]["by"] != nil {
				t.Fatalf("resolved: %v", ev)
			}
		})
	}
}

// A client on protocol 2026-07-28 may not be sent requests while a call
// runs: the call returns "input required" with the approval and continues
// when the client calls again with the answer (SEP-2322). Approve, deny,
// and the app answering while the form is open.
func TestHarnessApprovesOnTheCurrentProtocol(t *testing.T) {
	srv, c, _ := elicitServer(t)
	keyID, _ := newKey(c, "Claude Code", "ask")
	ws, _ := srv.ws.Get("acme")
	s, err := srv.agents.ClientSession(ws, keyID, "Claude Code", "ask")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	var answer atomic.Value
	answer.Store(func() *mcp.ElicitResult {
		return &mcp.ElicitResult{Action: "accept", Content: map[string]any{"decision": "approve"}}
	})
	clientT, serverT := mcp.NewInMemoryTransports()
	if _, err := srv.agents.ClientServer(ws, s, "test").Connect(ctx, serverT, nil); err != nil {
		t.Fatal(err)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "agent", Version: "1"}, &mcp.ClientOptions{
		ElicitationHandler: func(ctx context.Context, req *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
			return answer.Load().(func() *mcp.ElicitResult)(), nil
		},
	})
	sess, err := client.Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	if v := sess.InitializeResult().ProtocolVersion; v < "2026-07-28" {
		t.Fatalf("negotiated %s", v)
	}

	if res, err := writeDoc(sess, "modern.md"); err != nil || res.IsError {
		t.Fatalf("approved: %v %s", err, textOf(res))
	}
	if _, err := os.Stat(filepath.Join(ws.Path, "docs", "modern.md")); err != nil {
		t.Fatal("the approved change did not run")
	}
	answer.Store(func() *mcp.ElicitResult {
		return &mcp.ElicitResult{Action: "accept", Content: map[string]any{"decision": "deny", "note": "later"}}
	})
	if res, err := writeDoc(sess, "modern-denied.md"); err != nil || !res.IsError || !strings.Contains(textOf(res), "later") {
		t.Fatalf("denied: %v %s", err, textOf(res))
	}

	// the user answers in the app while the form is still open in the client
	release := make(chan struct{})
	answer.Store(func() *mcp.ElicitResult {
		<-release
		return &mcp.ElicitResult{Action: "cancel"}
	})
	done := make(chan *mcp.CallToolResult, 1)
	go func() {
		res, _ := writeDoc(sess, "from-app.md")
		done <- res
	}()
	var aid string
	for i := 0; i < 200 && aid == ""; i++ {
		var got struct{ Events []map[string]any }
		c.must("GET", "/api/workspaces/acme/sessions/mcp-"+keyID, nil, &got)
		for _, e := range got.Events {
			if e["type"] == "approval_request" && e["callId"] != nil {
				id, _ := e["approvalId"].(string)
				resolved := false
				for _, f := range got.Events {
					resolved = resolved || (f["type"] == "approval_resolved" && f["approvalId"] == id)
				}
				if !resolved {
					aid = id
				}
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	if aid == "" {
		t.Fatal("no open approval")
	}
	c.must("POST", "/api/workspaces/acme/sessions/mcp-"+keyID+"/approvals/"+aid, map[string]any{"decision": "approve"}, nil)
	close(release) // the form closes without a choice; the call returns what ran
	select {
	case res := <-done:
		if res == nil || res.IsError {
			t.Fatalf("approved in the app: %v", res)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("the call did not return")
	}
	if _, err := os.Stat(filepath.Join(ws.Path, "docs", "from-app.md")); err != nil {
		t.Fatal("the change approved in the app did not run")
	}
}

// A paused call belongs to its access key: another key that sends its
// state gets an error and the approval stays open.
func TestPausedApprovalBelongsToItsKey(t *testing.T) {
	srv, c, _ := elicitServer(t)
	ws, _ := srv.ws.Get("acme")
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	connect := func(label string) (string, *mcp.ClientSession) {
		keyID, _ := newKey(c, label, "ask")
		s, err := srv.agents.ClientSession(ws, keyID, label, "ask")
		if err != nil {
			t.Fatal(err)
		}
		clientT, serverT := mcp.NewInMemoryTransports()
		if _, err := srv.agents.ClientServer(ws, s, "test").Connect(ctx, serverT, nil); err != nil {
			t.Fatal(err)
		}
		client := mcp.NewClient(&mcp.Implementation{Name: label, Version: "1"}, &mcp.ClientOptions{
			MultiRoundTrip:     &mcp.MultiRoundTripOptions{Disabled: true},
			ElicitationHandler: func(context.Context, *mcp.ElicitRequest) (*mcp.ElicitResult, error) { return nil, nil },
		})
		sess, err := client.Connect(ctx, clientT, nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { sess.Close() })
		return keyID, sess
	}
	keyA, a := connect("Agent A")
	_, b := connect("Agent B")
	args := map[string]any{"path": "docs/a.md", "content": "# a\n"}
	first, err := a.CallTool(ctx, &mcp.CallToolParams{Name: "write_doc", Arguments: args})
	if err != nil || !first.NeedsInput() {
		t.Fatalf("first: %v %+v", err, first)
	}
	approve := mcp.InputResponseMap{"opsdeck": &mcp.ElicitResult{Action: "accept", Content: map[string]any{"decision": "approve"}}}
	stolen, err := b.CallTool(ctx, &mcp.CallToolParams{Name: "write_doc", Arguments: args, RequestState: first.RequestState, InputResponses: approve})
	if err != nil || !stolen.IsError || !strings.Contains(textOf(stolen), "no longer open") {
		t.Fatalf("another key: %v %q", err, textOf(stolen))
	}
	if _, err := os.Stat(filepath.Join(ws.Path, "docs", "a.md")); err == nil {
		t.Fatal("another key's answer ran the change")
	}
	if ev := resolvedEvents(c, keyA); len(ev) != 0 {
		t.Fatalf("the approval was resolved: %v", ev)
	}
	own, err := a.CallTool(ctx, &mcp.CallToolParams{Name: "write_doc", Arguments: args, RequestState: first.RequestState, InputResponses: approve})
	if err != nil || own.IsError {
		t.Fatalf("own: %v %q", err, textOf(own))
	}
}
