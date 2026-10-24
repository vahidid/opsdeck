package server

import (
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"testing"
	"time"

	"opsdeck/internal/paths"
	"opsdeck/internal/vault"
	"opsdeck/internal/workspace"
)

func newMachine(t *testing.T, master string) *client {
	t.Helper()
	t.Setenv("OPSDECK_HOME", t.TempDir())
	srv, err := New(paths.Default(), Options{Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { srv.Close() })
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	jar, _ := cookiejar.New(nil)
	c := &client{t: t, base: ts.URL, http: &http.Client{Jar: jar, Timeout: 60 * time.Second}}
	c.must("POST", "/api/auth/login", map[string]string{"token": srv.token}, nil)
	c.must("POST", "/api/vault/init", map[string]string{"password": master}, nil)
	return c
}

// TestWorkspaceExportImport: a workspace moves to another machine with its
// hosts and vault items; the receiving vault keeps its own password.
func TestWorkspaceExportImport(t *testing.T) {
	a := newMachine(t, "alice master pw")
	a.must("POST", "/api/workspaces", map[string]string{"name": "acme", "title": "ACME"}, nil)
	a.must("POST", "/api/workspaces/acme/resources", map[string]any{"kind": "Host", "name": "app-1",
		"spec": map[string]any{"address": "10.0.0.5", "auth": "vault://acme/deploy-key"}}, nil)
	a.must("POST", "/api/vault/items", map[string]any{"scope": "acme", "type": "login", "name": "deploy-key",
		"fields": []map[string]any{{"name": "username", "value": "deploy"}, {"name": "password", "value": "s3cret-pass"}}}, nil)
	if code := a.do("POST", "/api/workspaces/acme/export", map[string]string{"password": "short"}, nil); code != 400 {
		t.Fatalf("short password: %d", code)
	}
	var file json.RawMessage
	a.must("POST", "/api/workspaces/acme/export", map[string]string{"password": "share password"}, &file)

	b := newMachine(t, "bob master pw")
	if code := b.do("POST", "/api/workspaces/import-file", map[string]any{"bundle": file, "password": "wrong password"}, nil); code != 400 {
		t.Fatalf("wrong password: %d (401 would sign the UI out)", code)
	}
	var res struct {
		Workspace wsSummary
		Items     int
	}
	b.must("POST", "/api/workspaces/import-file", map[string]any{"bundle": file, "password": "share password"}, &res)
	if res.Workspace.Name != "acme" || res.Items != 1 || res.Workspace.HostCount != 1 {
		t.Fatalf("import: %+v", res)
	}
	var issues []workspace.Issue
	b.must("GET", "/api/workspaces/acme/validate", nil, &issues)
	for _, i := range issues {
		if i.Level == "error" {
			t.Errorf("validation after import: %+v", i)
		}
	}
	b.must("POST", "/api/vault/lock", nil, nil)
	b.must("POST", "/api/vault/unlock", map[string]string{"password": "bob master pw"}, nil)
	var items []vault.Item
	b.must("GET", "/api/vault/items?scope=acme", nil, &items)
	if len(items) != 1 || items[0].Name != "deploy-key" {
		t.Fatalf("items: %+v", items)
	}
	var revealed struct{ Value string }
	b.must("POST", "/api/vault/items/"+items[0].ID+"/reveal", map[string]string{"field": "password"}, &revealed)
	if revealed.Value != "s3cret-pass" {
		t.Fatalf("revealed %q", revealed.Value)
	}
}
