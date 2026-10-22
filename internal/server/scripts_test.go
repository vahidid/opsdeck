package server

import (
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"opsdeck/internal/paths"
	"opsdeck/internal/testutil/fakellm"
	"opsdeck/internal/testutil/sshd"
)

func TestScripts(t *testing.T) {
	home := t.TempDir()
	t.Setenv("OPSDECK_HOME", home)
	srv, err := New(paths.Default(), Options{Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	srv.agents.SetMCPBase(ts.URL + "/mcp/")
	jar, _ := cookiejar.New(nil)
	c := &client{t: t, base: ts.URL, http: &http.Client{Jar: jar, Timeout: 60 * time.Second}}
	c.must("POST", "/api/auth/login", map[string]string{"token": srv.token}, nil)
	c.must("POST", "/api/vault/init", map[string]string{"password": "correct horse battery"}, nil)
	c.must("POST", "/api/workspaces", map[string]string{"name": "acme", "title": "ACME"}, nil)
	var key map[string]any
	c.must("POST", "/api/vault/ssh/generate", map[string]any{"scope": "acme", "name": "deploy-key", "algorithm": "ed25519"}, &key)
	pub, _, _, _, _ := ssh.ParseAuthorizedKey([]byte(key["publicKey"].(string)))
	c.must("POST", "/api/vault/items", map[string]any{"scope": "acme", "type": "login", "name": "db",
		"fields": []map[string]any{{"name": "username", "value": "app"}, {"name": "password", "value": "S3cr3t-P4ssw0rd!"}},
		"policy": map[string]any{"agentUse": true}}, nil)

	workdir := t.TempDir()
	host, err := sshd.Start("127.0.0.1:0", sshd.Options{AuthorizedKeys: []ssh.PublicKey{pub}, Dir: workdir})
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close()
	_, port, _ := net.SplitHostPort(host.Addr)
	p, _ := strconv.Atoi(port)
	c.must("POST", "/api/workspaces/acme/resources", map[string]any{"kind": "Host", "name": "app-1", "environment": "dev",
		"spec": map[string]any{"address": "127.0.0.1", "port": p, "auth": "vault://acme/deploy-key"}}, nil)

	// --- the user's own script, uploaded from the Scripts page
	marker := filepath.Join(workdir, "bootstrapped")
	script := map[string]any{
		"description": "Initial setup of a new server",
		"when":        []string{"new-host"},
		"check":       "test -f " + marker,
		"params": []map[string]any{
			{"name": "DEPLOY_USER", "default": "deploy"},
			{"name": "DB_PASSWORD", "type": "secret", "required": true},
		},
		"body": "#!/bin/sh\nset -eu\nprintf '%s' \"$DB_PASSWORD\" > " + marker + "\necho \"user=$DEPLOY_USER pw=$DB_PASSWORD on $OPSDECK_HOST\"\n",
	}
	var saved map[string]any
	c.must("PUT", "/api/workspaces/acme/scripts/user/server-bootstrap", script, &saved)
	if saved["status"] != "approved" || saved["version"] != 1.0 || saved["scope"] != "user" {
		t.Fatalf("saved: %v", saved)
	}
	if _, err := os.Stat(filepath.Join(home, "data", "scripts", "server-bootstrap", "script.yaml")); err != nil {
		t.Fatal("user script not in the data folder")
	}
	if code := c.do("PUT", "/api/workspaces/acme/scripts/user/x", map[string]any{"description": "d", "body": "echo",
		"params": []map[string]any{{"name": "K", "type": "secret", "default": "hunter2"}}}, nil); code != 400 {
		t.Fatalf("literal secret default accepted: %d", code)
	}

	// --- run it from the page: the secret reaches the host, never the output
	var run struct {
		Results []map[string]any
	}
	c.must("POST", "/api/workspaces/acme/scripts/user/server-bootstrap/run", map[string]any{"target": "app-1",
		"params": map[string]string{"DB_PASSWORD": "vault://acme/db"}, "hash": saved["hash"]}, &run)
	r := run.Results[0]
	if r["exitCode"] != 0.0 || !strings.Contains(r["stdout"].(string), "user=deploy pw=[REDACTED") || strings.Contains(r["stdout"].(string), "S3cr3t") {
		t.Fatalf("run: %v", r)
	}
	if b, _ := os.ReadFile(marker); string(b) != "S3cr3t-P4ssw0rd!" {
		t.Fatalf("secret not injected: %q", b)
	}
	logPath, _ := r["log"].(string)
	if b, err := os.ReadFile(filepath.Join(home, "data", "workspaces", "acme", logPath)); err != nil || strings.Contains(string(b), "S3cr3t") {
		t.Fatalf("log artifact %q: %v", logPath, err)
	}
	// the check now passes: skipped
	c.must("POST", "/api/workspaces/acme/scripts/user/server-bootstrap/run", map[string]any{"target": "app-1",
		"params": map[string]string{"DB_PASSWORD": "vault://acme/db"}}, &run)
	if run.Results[0]["skipped"] != true {
		t.Fatalf("second run not skipped: %v", run.Results[0])
	}
	if code := c.do("POST", "/api/workspaces/acme/scripts/user/server-bootstrap/run", map[string]any{"target": "app-1",
		"params": map[string]string{"DB_PASSWORD": "vault://acme/db"}, "hash": "stale"}, nil); code != 409 {
		t.Fatalf("stale hash accepted: %d", code)
	}
	var runs []map[string]any
	c.must("GET", "/api/workspaces/acme/script-runs?script=server-bootstrap", nil, &runs)
	if len(runs) != 2 || runs[0]["skipped"] != true || runs[1]["params"].(map[string]any)["DEPLOY_USER"] != "deploy" {
		t.Fatalf("runs: %v", runs)
	}
	if _, ok := runs[1]["params"].(map[string]any)["DB_PASSWORD"]; ok {
		t.Fatal("secret parameter in the history")
	}

	// --- the agent: saves a draft, then running it asks even in auto mode
	os.Remove(marker)
	fake := fakellm.New([]fakellm.Turn{
		{Tools: []fakellm.ToolCall{{Name: "script", Args: map[string]any{"action": "save", "name": "touch-ready", "scope": "workspace",
			"description": "Mark a host as ready", "body": "#!/bin/sh\ntouch " + filepath.Join(workdir, "ready") + "\necho ready\n"}}}},
		{Tools: []fakellm.ToolCall{{Name: "run_script", Args: map[string]any{"script": "touch-ready", "target": "app-1"}}}},
		{Tools: []fakellm.ToolCall{{Name: "run_script", Args: map[string]any{"script": "server-bootstrap", "target": "app-1",
			"params": map[string]any{"DB_PASSWORD": "vault://acme/db"}}}}},
		{Text: "Done."},
	})
	defer fake.Close()
	var settings map[string]any
	c.must("GET", "/api/settings", nil, &settings)
	settings["agent"] = map[string]any{"autoServerChanges": true} // auto mode changes servers too
	settings["providers"] = []map[string]any{{"id": "fake", "type": "openai", "name": "Fake", "baseUrl": fake.URL, "models": []string{"m1"}}}
	c.must("PUT", "/api/settings", settings, nil)
	var sess map[string]any
	c.must("POST", "/api/workspaces/acme/sessions", map[string]any{"mode": "auto"}, &sess)
	sid := sess["id"].(string)
	c.must("POST", "/api/workspaces/acme/sessions/"+sid+"/messages", map[string]string{"text": "app-1 is new, set it up"}, nil)
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
				if ev["risk"] != "destructive" || !strings.Contains(ev["preview"].(string), "touch-ready@v1") {
					t.Fatalf("draft run approval: %v", ev)
				}
				c.must("POST", "/api/workspaces/acme/sessions/"+sid+"/approvals/"+ev["approvalId"].(string), map[string]string{"decision": "approve"}, nil)
			}
		}
		if full.Session["state"] == "idle" && len(full.Events) > 6 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if approvals != 1 {
		t.Fatalf("expected one approval (the draft), got %d", approvals)
	}
	if _, err := os.Stat(filepath.Join(workdir, "ready")); err != nil {
		t.Fatal("approved draft did not run")
	}
	if b, _ := os.ReadFile(marker); string(b) != "S3cr3t-P4ssw0rd!" {
		t.Fatal("approved user script did not run in auto mode")
	}
	for _, tr := range fake.LastToolResults() {
		if strings.Contains(tr, "S3cr3t") {
			t.Fatal("secret reached the model")
		}
	}
	if sys := fake.LastSystem(); !strings.Contains(sys, "server-bootstrap@v1 (user; approved; when: new-host; params: DEPLOY_USER, DB_PASSWORD)") ||
		!strings.Contains(sys, "touch-ready@v1 (workspace; draft") {
		t.Fatalf("scripts not in the system prompt:\n%s", sys)
	}

	// --- the draft is approved on the page; the user script is pinned into the workspace
	var list struct{ User, Workspace []map[string]any }
	c.must("GET", "/api/workspaces/acme/scripts", nil, &list)
	if len(list.User) != 1 || len(list.Workspace) != 1 || list.Workspace[0]["status"] != "draft" || list.Workspace[0]["createdBy"] != "agent" {
		t.Fatalf("list: %v", list)
	}
	c.must("POST", "/api/workspaces/acme/scripts/workspace/touch-ready/approve", map[string]any{"hash": list.Workspace[0]["hash"]}, &saved)
	if saved["status"] != "approved" {
		t.Fatalf("approve: %v", saved)
	}
	c.must("POST", "/api/workspaces/acme/scripts/user/server-bootstrap/copy", nil, &saved)
	if saved["scope"] != "workspace" || saved["status"] != "approved" {
		t.Fatalf("pin: %v", saved)
	}
	if _, err := os.Stat(filepath.Join(home, "data", "workspaces", "acme", "scripts", "server-bootstrap", "script.sh")); err != nil {
		t.Fatal("pinned script not in the workspace folder")
	}
	if b, _ := os.ReadFile(filepath.Join(home, "data", "workspaces", "acme", ".opsdeck", "script-approvals.json")); !strings.Contains(string(b), "touch-ready") {
		t.Fatalf("workspace approvals not kept in .opsdeck: %s", b)
	}
	c.must("DELETE", "/api/workspaces/acme/scripts/workspace/server-bootstrap", nil, nil)
}
