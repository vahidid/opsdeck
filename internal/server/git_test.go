package server

import (
	"bufio"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"
	"time"

	"opsdeck/internal/paths"
	"opsdeck/internal/testutil/fakellm"
	"opsdeck/internal/testutil/gitserver"
)

// TestGitAgent: a deployment repository on a self-hosted GitLab (HTTPS,
// private CA, token). The REST API clones it and lists its docs; the agent
// reads it, branches, edits, commits, pushes and opens and merges a merge
// request. Local edits are approved once for the session, pushing and
// opening the merge request ask, merging always asks.
func TestGitAgent(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	t.Setenv("HOME", t.TempDir())
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
	c.must("POST", "/api/workspaces", map[string]string{"name": "acme"}, nil)

	gs := gitserver.New(t)
	gl := httptest.NewTLSServer(gs.Handler(t))
	defer gl.Close()
	ca := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: gl.Certificate().Raw}))

	c.must("POST", "/api/vault/items", map[string]any{"scope": "acme", "type": "token", "name": "gitlab-deploy",
		"fields": []map[string]any{{"name": "token", "value": gitserver.Token}}, "policy": map[string]any{"agentUse": true}}, nil)
	c.must("POST", "/api/workspaces/acme/resources", map[string]any{"kind": "GitRemote", "name": "deploy", "environment": "prod",
		"description": "Kubernetes manifests of the shop",
		"spec":        map[string]any{"url": gl.URL + "/acme/deploy.git", "token": "vault://acme/gitlab-deploy", "caCert": ca, "docs": true, "provider": "gitlab"}}, nil)

	var check map[string]any
	c.must("POST", "/api/workspaces/acme/repos/deploy/check", nil, &check)
	if check["ok"] != true || check["defaultBranch"] != "main" || check["provider"] != "gitlab" || check["canPush"] != true {
		t.Fatalf("check: %v", check)
	}
	c.must("POST", "/api/workspaces/acme/repos/deploy/clone", map[string]any{}, nil)
	var repos []map[string]any
	c.must("GET", "/api/workspaces/acme/repos", nil, &repos)
	if st := repos[0]["status"].(map[string]any); st["branch"] != "main" || repos[0]["auth"] != "token" || repos[0]["last"] == nil {
		t.Fatalf("repos: %v", repos)
	}
	var docs []map[string]any
	c.must("GET", "/api/workspaces/acme/docs", nil, &docs)
	found := false
	for _, d := range docs {
		found = found || d["path"] == "repo/deploy/docs/runbook.md"
	}
	if !found {
		t.Fatalf("repo docs missing: %v", docs)
	}

	fake := fakellm.New([]fakellm.Turn{
		{Tools: []fakellm.ToolCall{{Name: "repo", Args: map[string]any{"action": "list"}}}},
		{Tools: []fakellm.ToolCall{{Name: "read_doc", Args: map[string]any{"path": "repo/deploy/docs/runbook.md"}}}},
		{Tools: []fakellm.ToolCall{{Name: "repo_files", Args: map[string]any{"action": "read", "path": "k8s/app.yaml"}}}},
		{Tools: []fakellm.ToolCall{{Name: "repo", Args: map[string]any{"action": "pull"}}}},
		{Tools: []fakellm.ToolCall{{Name: "repo", Args: map[string]any{"action": "checkout", "branch": "opsdeck/scale-shop", "create": true}}}},
		{Tools: []fakellm.ToolCall{{Name: "repo_write", Args: map[string]any{"action": "edit", "path": "k8s/app.yaml", "old_string": "replicas: 2", "new_string": "replicas: 4"}}}},
		{Tools: []fakellm.ToolCall{{Name: "repo", Args: map[string]any{"action": "commit", "message": "Scale the shop to 4 replicas"}}}},
		{Tools: []fakellm.ToolCall{{Name: "repo", Args: map[string]any{"action": "push"}}}},
		{Tools: []fakellm.ToolCall{{Name: "merge_request", Args: map[string]any{"action": "create", "title": "Scale the shop to 4 replicas", "description": "Black Friday load."}}}},
		{Tools: []fakellm.ToolCall{{Name: "merge_request", Args: map[string]any{"action": "merge", "id": 1}}}},
		{Tools: []fakellm.ToolCall{{Name: "repo", Args: map[string]any{"action": "push", "branch": "main"}}}},
		{Text: "Done."},
	})
	defer fake.Close()
	var settings map[string]any
	c.must("GET", "/api/settings", nil, &settings)
	settings["providers"] = []map[string]any{{"id": "fake", "type": "openai", "name": "Fake", "baseUrl": fake.URL, "models": []string{"m1"}}}
	c.must("PUT", "/api/settings", settings, nil)
	var sess map[string]any
	c.must("POST", "/api/workspaces/acme/sessions", map[string]any{"mode": "ask"}, &sess)
	sid := sess["id"].(string)
	stream, err := c.http.Get(ts.URL + "/api/workspaces/acme/sessions/" + sid + "/stream")
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Body.Close()
	c.must("POST", "/api/workspaces/acme/sessions/"+sid+"/messages", map[string]string{"text": "scale the shop to 4 replicas"}, nil)

	var asked []string
	previews := map[string]string{}
	results := map[string]string{}
	started := false
	sc := bufio.NewScanner(stream.Body)
	sc.Buffer(make([]byte, 1<<20), 8<<20)
	for sc.Scan() {
		d, ok := strings.CutPrefix(sc.Text(), "data: ")
		if !ok {
			continue
		}
		var ev map[string]any
		json.Unmarshal([]byte(d), &ev)
		switch ev["type"] {
		case "approval_request":
			tool := ev["tool"].(string)
			asked = append(asked, tool)
			preview, _ := ev["preview"].(string)
			previews[ev["callId"].(string)] = preview
			decision := "approve"
			switch {
			case tool == "repo" && strings.Contains(preview, "create branch"):
				decision = "always" // covers the edit and the commit
			case tool == "repo" && strings.HasPrefix(preview, "push main"):
				decision = "deny"
			}
			c.must("POST", "/api/workspaces/acme/sessions/"+sid+"/approvals/"+ev["approvalId"].(string), map[string]string{"decision": decision}, nil)
		case "tool_result":
			results[ev["callId"].(string)], _ = ev["output"].(string)
		case "status":
			if ev["state"] == "running" {
				started = true
			}
		}
		if started && ev["type"] == "status" && ev["state"] == "idle" {
			break
		}
	}
	if got := strings.Join(asked, ","); got != "repo,repo,merge_request,merge_request,repo" {
		t.Fatalf("approvals asked: %s", got)
	}
	expect := func(id, sub string) {
		t.Helper()
		if !strings.Contains(results[id], sub) {
			t.Errorf("%s: want %q in %q", id, sub, results[id])
		}
	}
	expect("call_0_0", "deploy: https://127.0.0.1")
	expect("call_0_0", "on main")
	expect("call_1_0", "rollout undo")
	expect("call_2_0", "replicas: 2")
	expect("call_3_0", "already up to date")
	expect("call_4_0", "on opsdeck/scale-shop")
	expect("call_5_0", "edited deploy:k8s/app.yaml")
	expect("call_6_0", "committed")
	expect("call_7_0", "pushed opsdeck/scale-shop")
	expect("call_8_0", "opened !1 Scale the shop to 4 replicas")
	expect("call_9_0", "merged")
	if !strings.Contains(previews["call_7_0"], "Scale the shop to 4 replicas") || !strings.Contains(previews["call_7_0"], "+replicas: 4") {
		t.Errorf("push preview: %s", previews["call_7_0"])
	}
	if !strings.Contains(previews["call_10_0"], "push main") {
		t.Errorf("protected push preview: %s", previews["call_10_0"])
	}
	if got := gitserver.Git(t, gs.Bare, "show", "main:k8s/app.yaml"); !strings.Contains(got, "replicas: 4") {
		t.Fatalf("main after merge: %s", got)
	}
	if got := gitserver.Git(t, gs.Bare, "log", "-1", "--format=%an%n%B", "opsdeck/scale-shop"); !strings.Contains(got, "Deploy Bot") || !strings.Contains(got, "Opsdeck-Agent-Session: "+sid) {
		t.Fatalf("agent commit: %s", got)
	}
	if !strings.Contains(fake.LastSystem(), "repo/deploy/docs/runbook.md") || !strings.Contains(fake.LastSystem(), "merge request") {
		t.Error("system prompt should list repository docs and explain the Git workflow")
	}

	// The user's view: history, merge requests, audit.
	var log []map[string]any
	c.must("GET", "/api/workspaces/acme/repos/deploy/log?ref=opsdeck/scale-shop&n=5", nil, &log)
	if log[0]["subject"] != "Scale the shop to 4 replicas" {
		t.Fatalf("log: %v", log)
	}
	var mrs map[string]any
	c.must("GET", "/api/workspaces/acme/repos/deploy/mrs?state=merged", nil, &mrs)
	if items := mrs["items"].([]any); len(items) != 1 {
		t.Fatalf("mrs: %v", mrs)
	}
	c.must("POST", "/api/workspaces/acme/repos/deploy/checkout", map[string]any{"branch": "main"}, nil)
	var pulled map[string]string
	c.must("POST", "/api/workspaces/acme/repos/deploy/pull", map[string]any{}, &pulled)
	if !strings.Contains(pulled["output"], "updated") {
		t.Fatalf("pull: %v", pulled)
	}
	var file map[string]any
	c.must("GET", "/api/workspaces/acme/repos/deploy/file?path=k8s/app.yaml", nil, &file)
	if !strings.Contains(file["text"].(string), "replicas: 4") {
		t.Fatalf("file: %v", file)
	}
	var entries []map[string]any
	c.must("GET", "/api/audit?workspace=acme&limit=200", nil, &entries)
	actions := map[string]bool{}
	for _, e := range entries {
		actions[e["action"].(string)] = true
	}
	for _, a := range []string{"git.clone", "git.pull", "git.check"} {
		if !actions[a] {
			t.Errorf("audit misses %s: %v", a, actions)
		}
	}
}
