package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"opsdeck/internal/paths"
	"opsdeck/internal/server"
	"opsdeck/internal/testutil/fakellm"
)

// setup prepares opsdeck in a fresh home: a vault, a workspace and a
// scripted model. It returns a client of a server for it.
func setup(t *testing.T, fake *fakellm.Server) (*apiClient, func()) {
	t.Helper()
	srv, err := server.New(paths.Default(), server.Options{Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	tok, _ := os.ReadFile(filepath.Join(paths.Default().State, "server.token"))
	c := &apiClient{base: ts.URL, token: strings.TrimSpace(string(tok)), hc: &http.Client{Timeout: 30 * time.Second}}
	ctx := context.Background()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(c.do(ctx, "POST", "/api/vault/init", map[string]string{"password": "correct horse battery"}, nil))
	must(c.do(ctx, "POST", "/api/workspaces", map[string]string{"name": "acme", "title": "ACME"}, nil))
	var settings map[string]any
	must(c.do(ctx, "GET", "/api/settings", nil, &settings))
	settings["providers"] = []map[string]any{{"id": "fake", "type": "openai", "name": "Fake", "baseUrl": fake.URL, "models": []string{"m1"}}}
	must(c.do(ctx, "PUT", "/api/settings", settings, nil))
	return c, func() { ts.Close(); srv.Close() }
}

// capture runs fn with stdout going to a buffer.
func capture(t *testing.T, fn func()) string {
	t.Helper()
	r, w, _ := os.Pipe()
	old := os.Stdout
	os.Stdout = w
	done := make(chan string)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()
	fn()
	w.Close()
	os.Stdout = old
	return <-done
}

func TestRunHeadless(t *testing.T) {
	t.Setenv("OPSDECK_HOME", t.TempDir())
	fake := fakellm.New([]fakellm.Turn{
		{Text: "Looking.", Tools: []fakellm.ToolCall{{Name: "inventory", Args: map[string]any{}}, {Name: "memory", Args: map[string]any{"action": "append", "content": "x"}}}},
		{Text: "The workspace is empty."},
	})
	defer fake.Close()
	c, closeFn := setup(t, fake)
	defer closeFn()
	var code int
	var err error
	out := capture(t, func() {
		code, err = c.run(context.Background(), runOpts{prompt: "what is in the workspace?", ws: "acme", mode: "ask", output: "json", waitTasks: true})
	})
	if err != nil {
		t.Fatal(err)
	}
	var sum runSummary
	if json.Unmarshal([]byte(out), &sum) != nil || sum.Result != "The workspace is empty." || sum.ToolCalls != 2 || sum.Denied != 1 {
		t.Fatalf("summary: %s", out)
	}
	if code != 3 {
		t.Fatalf("exit code %d, want 3 (a change was denied)", code)
	}
	var sess struct {
		Session struct{ Headless bool }
	}
	c.do(context.Background(), "GET", "/api/workspaces/acme/sessions/"+sum.Session, nil, &sess)
	if !sess.Session.Headless {
		t.Fatal("session not marked headless")
	}
}

func TestRunStartsOpsdeckWhenNoneRuns(t *testing.T) {
	t.Setenv("OPSDECK_HOME", t.TempDir())
	fake := fakellm.New([]fakellm.Turn{{Text: "Hello from the agent."}})
	defer fake.Close()
	_, closeFn := setup(t, fake)
	closeFn() // nothing runs now; the vault is locked on disk

	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	ctx := context.Background()
	if _, _, err := connectServer(ctx, port); err == nil || !strings.Contains(err.Error(), "OPSDECK_VAULT_PASSWORD") {
		t.Fatalf("locked vault without a password: %v", err)
	}
	t.Setenv("OPSDECK_VAULT_PASSWORD", "correct horse battery")
	c, stop, err := connectServer(ctx, port)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	var buf bytes.Buffer
	out := capture(t, func() {
		code, err := c.run(ctx, runOpts{prompt: "hi", ws: "acme", mode: "plan", output: "text"})
		if err != nil || code != 0 {
			t.Errorf("run: %d %v", code, err)
		}
	})
	buf.WriteString(out)
	if strings.TrimSpace(buf.String()) != "Hello from the agent." {
		t.Fatalf("output: %q", buf.String())
	}
}
