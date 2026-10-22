package server

import (
	"encoding/pem"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"opsdeck/internal/paths"
	"opsdeck/internal/testutil/gitserver"
)

// TestGitConnectors: connect a self-hosted GitLab account (token, an
// existing glab login, browser sign-in), list its repositories from a
// workspace, add one by picking it and clone it with nothing else to set.
func TestGitConnectors(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	t.Setenv("HOME", t.TempDir())
	t.Setenv("OPSDECK_HOME", t.TempDir())
	// a logged-in glab for 127.0.0.1
	bin := t.TempDir()
	os.WriteFile(filepath.Join(bin, "glab"), []byte("#!/bin/sh\n[ \"$1 $2 $3 $4 $5\" = \"config get token --host 127.0.0.1\" ] && echo "+gitserver.Token+" && exit 0\nexit 1\n"), 0o755)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

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
	c.must("POST", "/api/workspaces", map[string]string{"name": "acme"}, nil)

	gs := gitserver.New(t)
	gl := httptest.NewTLSServer(gs.Handler(t))
	defer gl.Close()
	ca := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: gl.Certificate().Raw}))
	host := strings.TrimPrefix(gl.URL, "https://")

	// What can be used to connect.
	var opts map[string]any
	c.must("POST", "/api/git/connections/options", map[string]any{"provider": "gitlab", "url": host + "/some/path"}, &opts)
	if opts["url"] != gl.URL || !strings.HasPrefix(opts["tokenPage"].(string), gl.URL+"/-/user_settings/personal_access_tokens?") ||
		opts["cli"].(map[string]any)["ready"] != true {
		t.Fatalf("options: %v", opts)
	}

	// A wrong token is refused; the glab login works.
	if msg := failure(c, "POST", "/api/git/connections", map[string]any{"provider": "gitlab", "url": gl.URL, "caCert": ca, "token": "nope"}); !strings.Contains(msg, "rejected") {
		t.Fatalf("bad token: %v", msg)
	}
	var conn map[string]any
	c.must("POST", "/api/git/connections", map[string]any{"provider": "gitlab", "url": gl.URL, "caCert": ca, "method": "cli"}, &conn)
	id := conn["id"].(string)
	if conn["login"] != "deploy-bot" || conn["method"] != "glab" || !strings.HasPrefix(conn["credential"].(string), "vault://global/git-") {
		t.Fatalf("connection: %v", conn)
	}
	var test map[string]any
	c.must("POST", "/api/git/connections/"+id+"/test", nil, &test)
	if test["ok"] != true || test["login"] != "deploy-bot" {
		t.Fatalf("test: %v", test)
	}

	// Browser sign-in for the same server replaces it (same ID).
	var flow map[string]any
	c.must("POST", "/api/git/oauth", map[string]any{"provider": "gitlab", "url": gl.URL, "caCert": ca, "clientId": "app1"}, &flow)
	if flow["userCode"] != "WDJB-MJHT" {
		t.Fatalf("flow: %v", flow)
	}
	var st map[string]any
	for i := 0; i < 40; i++ {
		c.must("GET", "/api/git/oauth/"+flow["flow"].(string), nil, &st)
		if st["status"] != "pending" {
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	if st["status"] != "done" || st["connection"].(map[string]any)["id"] != id || st["connection"].(map[string]any)["method"] != "oauth" {
		t.Fatalf("oauth: %v", st)
	}
	var list []map[string]any
	c.must("GET", "/api/git/connections", nil, &list)
	if len(list) != 1 {
		t.Fatalf("connections: %v", list)
	}

	// Pick a repository from the account and add it to the workspace.
	var repos []map[string]any
	c.must("GET", "/api/workspaces/acme/git/connections/"+id+"/repos?q=deploy", nil, &repos)
	if len(repos) != 1 || repos[0]["fullName"] != "acme/deploy" {
		t.Fatalf("repos: %v", repos)
	}
	c.must("POST", "/api/workspaces/acme/resources", map[string]any{"kind": "GitRemote", "name": "deploy",
		"spec": map[string]any{"connection": id, "url": repos[0]["httpUrl"]}}, nil)
	c.must("POST", "/api/workspaces/acme/repos/deploy/clone", map[string]any{}, nil)
	var mrs map[string]any
	c.must("GET", "/api/workspaces/acme/repos/deploy/mrs", nil, &mrs)
	if mrs["provider"] != "gitlab" {
		t.Fatalf("mrs: %v", mrs)
	}

	// Disconnecting removes the token from the vault.
	c.must("DELETE", "/api/git/connections/"+id, nil, nil)
	var items []map[string]any
	c.must("GET", "/api/vault/items?scope=global", nil, &items)
	for _, it := range items {
		if strings.HasPrefix(it["name"].(string), "git-") {
			t.Fatalf("token left in the vault: %v", it["name"])
		}
	}
	if msg := failure(c, "POST", "/api/workspaces/acme/repos/deploy/fetch", nil); !strings.Contains(msg, "Connectors") {
		t.Fatalf("fetch without connector: %v", msg)
	}
}

// failure returns the error of a request that must fail.
func failure(c *client, method, path string, body any) string {
	c.t.Helper()
	var e struct{ Error string }
	if code := c.do(method, path, body, &e); code < 400 {
		c.t.Fatalf("%s %s should fail, got %d", method, path, code)
	}
	return e.Error
}
