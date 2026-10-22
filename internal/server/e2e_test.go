package server

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"golang.org/x/crypto/ssh"

	"opsdeck/internal/paths"
	"opsdeck/internal/testutil/fakellm"
	"opsdeck/internal/testutil/sshd"
)

type client struct {
	t    *testing.T
	base string
	http *http.Client
}

func (c *client) do(method, path string, body any, out any) int {
	c.t.Helper()
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, c.base+path, r)
	req.Header.Set("Content-Type", "application/json")
	res, err := c.http.Do(req)
	if err != nil {
		c.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	if out != nil && len(b) > 0 {
		if err := json.Unmarshal(b, out); err != nil {
			c.t.Fatalf("%s %s: decode %q: %v", method, path, b, err)
		}
	}
	if res.StatusCode >= 400 && out == nil {
		c.t.Logf("%s %s -> %d %s", method, path, res.StatusCode, b)
	}
	return res.StatusCode
}

func (c *client) must(method, path string, body any, out any) {
	c.t.Helper()
	var raw json.RawMessage
	code := c.do(method, path, body, &raw)
	if code >= 300 {
		c.t.Fatalf("%s %s -> %d %s", method, path, code, raw)
	}
	if out != nil {
		json.Unmarshal(raw, out)
	}
}

func TestEndToEnd(t *testing.T) {
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

	// --- auth
	if code := c.do("GET", "/api/vault/status", nil, nil); code != 401 {
		t.Fatalf("expected 401 before login, got %d", code)
	}
	if code := c.do("POST", "/api/auth/login", map[string]string{"token": "nope"}, nil); code != 401 {
		t.Fatalf("bad token accepted: %d", code)
	}
	c.must("POST", "/api/auth/login", map[string]string{"token": srv.token}, nil)

	// cross-origin request is refused
	req, _ := http.NewRequest("GET", ts.URL+"/api/vault/status", nil)
	req.Header.Set("Origin", "https://evil.example")
	if res, _ := c.http.Do(req); res.StatusCode != 403 {
		t.Fatalf("cross-origin allowed: %d", res.StatusCode)
	}

	// --- vault
	var st map[string]any
	if code := c.do("GET", "/api/workspaces", nil, &st); code != 423 {
		t.Fatalf("expected 423 before init, got %d", code)
	}
	c.must("POST", "/api/vault/init", map[string]string{"password": "correct horse battery"}, &st)
	if st["unlocked"] != true {
		t.Fatal("vault not unlocked after init")
	}

	// --- workspace
	c.must("POST", "/api/workspaces", map[string]string{"name": "acme", "title": "ACME", "color": "#e4572e"}, nil)
	var key map[string]any
	c.must("POST", "/api/vault/ssh/generate", map[string]any{"scope": "acme", "name": "deploy-key", "algorithm": "ed25519"}, &key)
	if key["publicKey"] == "" || key["fields"] == nil {
		t.Fatalf("bad key item: %v", key)
	}
	for _, f := range key["fields"].([]any) {
		fm := f.(map[string]any)
		if fm["name"] == "private_key" && fm["value"] != nil {
			t.Fatal("private key value leaked in item listing")
		}
	}
	var listed []map[string]any
	c.must("GET", "/api/vault/items?scope=acme", nil, &listed)
	if _, ok := listed[0]["tags"].([]any); !ok {
		t.Fatalf("tags must be an array, got %v", listed[0]["tags"])
	}
	pub, _, _, _, err := ssh.ParseAuthorizedKey([]byte(key["publicKey"].(string)))
	if err != nil {
		t.Fatal(err)
	}

	// --- a fake docker CLI on the hosts' PATH
	bin := t.TempDir()
	os.WriteFile(filepath.Join(bin, "docker"), []byte(`#!/bin/sh
case "$1" in
  version) echo 27.0.0;;
  ps) echo '{"ID":"0123456789abcdef","Names":"web","Image":"nginx:1.27","State":"running","Status":"Up 2 hours","Ports":"0.0.0.0:80->80/tcp","CreatedAt":"2026-10-01"}';;
  images) echo '{"ID":"sha256:abc","Repository":"nginx","Tag":"1.27","Size":"187MB","CreatedSince":"2 weeks ago"}';;
  stats) echo '{"ID":"0123456789ab","Name":"web","CPUPerc":"0.50%","MemUsage":"20MiB / 1GiB","MemPerc":"2%","NetIO":"1kB / 2kB","BlockIO":"0B / 0B"}';;
  inspect) echo '[{"Id":"0123456789abcdef","Name":"/web"}]';;
  restart) echo "$2";;
  *) echo "unsupported $1" >&2; exit 1;;
esac
`), 0o755)
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))

	// --- two SSH servers: bastion and app-1 (reached only through the bastion)
	workdir := t.TempDir()
	bastion, err := sshd.Start("127.0.0.1:0", sshd.Options{AuthorizedKeys: []ssh.PublicKey{pub}, Dir: workdir})
	if err != nil {
		t.Fatal(err)
	}
	defer bastion.Close()
	app, err := sshd.Start("127.0.0.1:0", sshd.Options{AuthorizedKeys: []ssh.PublicKey{pub}, Dir: workdir})
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	_, bport, _ := net.SplitHostPort(bastion.Addr)
	_, aport, _ := net.SplitHostPort(app.Addr)
	bp, _ := strconv.Atoi(bport)
	ap, _ := strconv.Atoi(aport)

	c.must("POST", "/api/workspaces/acme/resources", map[string]any{"kind": "Host", "name": "bastion", "environment": "prod",
		"spec": map[string]any{"address": "127.0.0.1", "port": bp, "auth": "vault://acme/deploy-key", "roles": []string{"bastion"}}}, nil)
	c.must("POST", "/api/workspaces/acme/resources", map[string]any{"kind": "Host", "name": "app-1", "environment": "dev", "via": "host/bastion",
		"spec": map[string]any{"address": "127.0.0.1", "port": ap, "auth": "vault://acme/deploy-key", "roles": []string{"app", "docker"}}}, nil)
	c.must("POST", "/api/workspaces/acme/resources", map[string]any{"kind": "HostGroup", "name": "apps", "spec": map[string]any{"roles": []string{"app"}}}, nil)

	// literal secrets are refused
	if code := c.do("POST", "/api/workspaces/acme/resources", map[string]any{"kind": "Panel", "name": "grafana",
		"spec": map[string]any{"url": "http://x", "password": "hunter2"}}, nil); code != 400 {
		t.Fatalf("literal secret accepted: %d", code)
	}

	var issues []map[string]any
	c.must("GET", "/api/workspaces/acme/validate", nil, &issues)
	for _, is := range issues {
		if is["level"] == "error" {
			t.Fatalf("validation error: %v", is)
		}
	}
	var resolved struct{ Path []map[string]any }
	c.must("GET", "/api/workspaces/acme/resolve/Host/app-1", nil, &resolved)
	if len(resolved.Path) != 1 || resolved.Path[0]["name"] != "bastion" {
		t.Fatalf("bad path: %v", resolved.Path)
	}

	// --- host operations through the jump host
	var check map[string]any
	c.must("POST", "/api/workspaces/acme/hosts/app-1/check", nil, &check)
	if check["reachable"] != true || check["ssh"] != true {
		t.Fatalf("check failed: %v", check)
	}
	var ex map[string]any
	c.must("POST", "/api/workspaces/acme/hosts/app-1/exec", map[string]any{"command": "echo hello-$((40+2))"}, &ex)
	if strings.TrimSpace(ex["stdout"].(string)) != "hello-42" {
		t.Fatalf("exec: %v", ex)
	}
	var stats map[string]any
	c.must("GET", "/api/workspaces/acme/hosts/app-1/stats", nil, &stats)
	if stats["hostname"] == "" || stats["memTotal"].(float64) <= 0 {
		t.Fatalf("stats: %v", stats)
	}
	target := filepath.Join(workdir, "app.conf")
	c.must("PUT", "/api/workspaces/acme/hosts/app-1/files/content", map[string]any{"path": target, "content": "port=80\n"}, nil)
	var fc map[string]any
	c.must("GET", "/api/workspaces/acme/hosts/app-1/files/content?path="+target, nil, &fc)
	if fc["content"] != "port=80\n" {
		t.Fatalf("file roundtrip: %v", fc)
	}
	var listing map[string]any
	c.must("GET", "/api/workspaces/acme/hosts/app-1/files?path="+workdir, nil, &listing)
	if len(listing["entries"].([]any)) == 0 {
		t.Fatal("empty listing")
	}

	// --- docker over SSH
	var containers []map[string]any
	c.must("GET", "/api/workspaces/acme/hosts/app-1/docker/containers?all=1", nil, &containers)
	if len(containers) != 1 || containers[0]["name"] != "web" || containers[0]["id"] != "0123456789ab" {
		t.Fatalf("containers: %v", containers)
	}
	var images, dstats []map[string]any
	c.must("GET", "/api/workspaces/acme/hosts/app-1/docker/images", nil, &images)
	c.must("GET", "/api/workspaces/acme/hosts/app-1/docker/stats", nil, &dstats)
	if images[0]["tag"] != "1.27" || dstats[0]["cpu"] != "0.50%" {
		t.Fatalf("images/stats: %v %v", images, dstats)
	}
	c.must("POST", "/api/workspaces/acme/hosts/app-1/docker/containers/web/restart", nil, nil)
	if code := c.do("POST", "/api/workspaces/acme/hosts/app-1/docker/containers/web;rm%20-rf/restart", nil, nil); code < 400 {
		t.Fatal("unsafe container id accepted")
	}

	// --- terminal over websocket
	wsURL := strings.Replace(ts.URL, "http://", "ws://", 1) + "/api/workspaces/acme/hosts/app-1/terminal?cols=100&rows=30"
	u, _ := http.NewRequest("GET", ts.URL, nil)
	hdr := http.Header{}
	for _, ck := range jar.Cookies(u.URL) {
		hdr.Add("Cookie", ck.Name+"="+ck.Value)
	}
	wsConn, _, err := websocket.DefaultDialer.Dial(wsURL, hdr)
	if err != nil {
		t.Fatal(err)
	}
	wsConn.WriteJSON(map[string]any{"t": "d", "d": "echo term-$((1+1))\n"})
	var out strings.Builder
	deadline := time.Now().Add(15 * time.Second)
	for !strings.Contains(out.String(), "term-2") && time.Now().Before(deadline) {
		wsConn.SetReadDeadline(deadline)
		var m map[string]any
		if err := wsConn.ReadJSON(&m); err != nil {
			t.Fatalf("terminal read: %v (got %q)", err, out.String())
		}
		if m["t"] == "e" {
			t.Fatalf("terminal error: %v", m["msg"])
		}
		if d, ok := m["d"].(string); ok {
			out.WriteString(d)
		}
	}
	wsConn.WriteJSON(map[string]any{"t": "d", "d": "exit\n"})
	wsConn.Close()

	// --- log streaming (file source)
	logFile := filepath.Join(workdir, "app.log")
	os.WriteFile(logFile, []byte("line one\nERROR line two\n"), 0o644)
	res, err := c.http.Get(ts.URL + "/api/workspaces/acme/logs/stream?host=app-1&source=file&path=" + logFile + "&lines=10")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if !strings.Contains(string(body), "ERROR line two") || !strings.Contains(string(body), "event: end") {
		t.Fatalf("log stream: %s", body)
	}

	// --- agent with a scripted model
	secretFile := filepath.Join(workdir, "leak.txt")
	fake := fakellm.New([]fakellm.Turn{
		{Text: "Checking the host.", Tools: []fakellm.ToolCall{
			{Name: "inventory", Args: map[string]any{}},
			{Name: "exec", Args: map[string]any{"target": "app-1", "command": "uname -s && cat " + secretFile}},
		}},
		{Tools: []fakellm.ToolCall{{Name: "exec", Args: map[string]any{"target": "app-1", "command": "touch " + filepath.Join(workdir, "changed")}}}},
		{Tools: []fakellm.ToolCall{{Name: "exec", Args: map[string]any{"target": "bastion", "command": "rm -rf " + filepath.Join(workdir, "nothing")}}}},
		{Text: "Done: the host runs Linux and the file was created."},
	})
	defer fake.Close()
	// a vault secret that a careless command prints
	c.must("POST", "/api/vault/items", map[string]any{"scope": "acme", "type": "login", "name": "db",
		"fields": []map[string]any{{"name": "username", "value": "root"}, {"name": "password", "value": "S3cr3t-P4ssw0rd!"}}}, nil)
	os.WriteFile(secretFile, []byte("db password is S3cr3t-P4ssw0rd! ok\n"), 0o644)

	var settings map[string]any
	c.must("GET", "/api/settings", nil, &settings)
	settings["providers"] = []map[string]any{{"id": "fake", "type": "openai", "name": "Fake", "baseUrl": fake.URL, "models": []string{"m1"}}}
	c.must("PUT", "/api/settings", settings, nil)
	var sess map[string]any
	c.must("POST", "/api/workspaces/acme/sessions", map[string]any{"mode": "ask"}, &sess)
	sid := sess["id"].(string)
	if sess["model"] != "fake/m1" {
		t.Fatalf("default model: %v", sess["model"])
	}

	// subscribe to the live stream
	streamRes, err := c.http.Get(ts.URL + "/api/workspaces/acme/sessions/" + sid + "/stream")
	if err != nil {
		t.Fatal(err)
	}
	live := make(chan map[string]any, 1000)
	go func() {
		sc := bufio.NewScanner(streamRes.Body)
		for sc.Scan() {
			if d, ok := strings.CutPrefix(sc.Text(), "data: "); ok {
				var ev map[string]any
				json.Unmarshal([]byte(d), &ev)
				live <- ev
			}
		}
		close(live)
	}()
	c.must("POST", "/api/workspaces/acme/sessions/"+sid+"/messages", map[string]string{"text": "check app-1 and create the marker file"}, nil)

	waitFor := func(pred func(ev map[string]any) bool, what string) map[string]any {
		t.Helper()
		timeout := time.After(30 * time.Second)
		for {
			select {
			case ev, ok := <-live:
				if !ok {
					t.Fatalf("stream closed while waiting for %s", what)
				}
				if pred(ev) {
					return ev
				}
			case <-timeout:
				t.Fatalf("timeout waiting for %s", what)
			}
		}
	}
	// read-only command runs without approval; its output is redacted
	r1 := waitFor(func(ev map[string]any) bool { return ev["type"] == "tool_result" && ev["callId"] == "call_0_1" }, "first exec result")
	if o := r1["output"].(string); strings.Contains(o, "S3cr3t-P4ssw0rd!") || !strings.Contains(o, "REDACTED") {
		t.Fatalf("secret not redacted: %q", o)
	}
	// the change waits for approval
	apr := waitFor(func(ev map[string]any) bool { return ev["type"] == "approval_request" }, "approval request")
	if apr["risk"] != "change" {
		t.Fatalf("expected change risk: %v", apr)
	}
	c.must("POST", "/api/workspaces/acme/sessions/"+sid+"/approvals/"+apr["approvalId"].(string), map[string]string{"decision": "approve"}, nil)
	waitFor(func(ev map[string]any) bool { return ev["type"] == "tool_result" && ev["callId"] == "call_1_0" }, "approved exec result")
	if _, err := os.Stat(filepath.Join(workdir, "changed")); err != nil {
		t.Fatal("approved command did not run")
	}
	// destructive on a protected environment asks; we deny it
	ap2 := waitFor(func(ev map[string]any) bool { return ev["type"] == "approval_request" }, "second approval")
	if ap2["risk"] != "destructive" || ap2["environment"] != "prod" {
		t.Fatalf("expected destructive/prod: %v", ap2)
	}
	c.must("POST", "/api/workspaces/acme/sessions/"+sid+"/approvals/"+ap2["approvalId"].(string), map[string]string{"decision": "deny", "note": "not today"}, nil)
	final := waitFor(func(ev map[string]any) bool {
		return ev["type"] == "assistant" && strings.HasPrefix(ev["text"].(string), "Done")
	}, "final answer")
	_ = final
	waitFor(func(ev map[string]any) bool { return ev["type"] == "status" && ev["state"] == "idle" }, "idle")
	streamRes.Body.Close()

	for _, tr := range fake.LastToolResults() {
		if strings.Contains(tr, "S3cr3t-P4ssw0rd!") {
			t.Fatal("secret reached the model")
		}
	}
	if !strings.Contains(fake.LastSystem(), "app-1") || strings.Contains(fake.LastSystem(), "PRIVATE KEY") {
		t.Fatal("system prompt should list inventory and never secrets")
	}
	var full struct {
		Session map[string]any
		Events  []map[string]any
	}
	c.must("GET", "/api/workspaces/acme/sessions/"+sid, nil, &full)
	if full.Session["title"] == "New session" || len(full.Events) < 8 {
		t.Fatalf("session not persisted properly: %v events", len(full.Events))
	}

	// --- git_sync in auto mode (dev environment: no approval needed)
	repoDir := filepath.Join(t.TempDir(), "shop.git")
	for _, args := range [][]string{
		{"init", "-q", "--bare", repoDir},
		{"clone", "-q", repoDir, repoDir + "-work"},
	} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %s", args, out)
		}
	}
	work := repoDir + "-work"
	os.WriteFile(filepath.Join(work, "Dockerfile"), []byte("FROM nginx:1.27\n"), 0o644)
	for _, args := range [][]string{{"add", "."}, {"-c", "user.email=t@t", "-c", "user.name=t", "commit", "-qm", "init"}, {"push", "-q", "origin", "HEAD:main"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = work
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %s", args, out)
		}
	}
	c.must("POST", "/api/vault/items", map[string]any{"scope": "acme", "type": "token", "name": "gitlab-token",
		"fields": []map[string]any{{"name": "token", "value": "glpat-SuperSecretToken123456"}}}, nil)
	c.must("POST", "/api/workspaces/acme/resources", map[string]any{"kind": "GitRemote", "name": "shop",
		"spec": map[string]any{"url": "file://" + repoDir, "branch": "main", "token": "vault://acme/gitlab-token"}}, nil)
	dest := filepath.Join(workdir, "apps", "shop")
	fake2 := fakellm.New([]fakellm.Turn{
		{Tools: []fakellm.ToolCall{{Name: "git_sync", Args: map[string]any{"repo": "shop", "host": "app-1", "path": dest}}}},
		{Tools: []fakellm.ToolCall{{Name: "git_sync", Args: map[string]any{"repo": "shop", "host": "app-1", "path": dest}}}},
		{Tools: []fakellm.ToolCall{
			{Name: "write_report", Args: map[string]any{"title": "Shop deployment on app-1", "content": "Cloned shop into the apps dir.", "tags": []string{"deploy"}}},
			{Name: "memory", Args: map[string]any{"action": "append", "content": "shop is deployed from GitRemote/shop into ~/apps/shop on app-1"}},
		}},
		{Tools: []fakellm.ToolCall{
			{Name: "save_artifact", Args: map[string]any{"source": "file", "host": "app-1", "path": filepath.Join(dest, "Dockerfile"), "note": "image base"}},
			{Name: "save_artifact", Args: map[string]any{"source": "command", "host": "app-1", "command": "cat " + secretFile, "name": "leak.txt"}},
			{Name: "save_artifact", Args: map[string]any{"source": "text", "name": "../../escape.csv", "content": "host,disk\napp-1,41%"}},
		}},
		{Text: "Cloned."},
	})
	defer fake2.Close()
	settings["providers"] = []map[string]any{{"id": "fake", "type": "openai", "name": "Fake", "baseUrl": fake2.URL, "models": []string{"m1"}}}
	c.must("PUT", "/api/settings", settings, nil)
	var s2 map[string]any
	c.must("POST", "/api/workspaces/acme/sessions", map[string]any{"mode": "auto"}, &s2)
	sid2 := s2["id"].(string)
	c.must("POST", "/api/workspaces/acme/sessions/"+sid2+"/messages", map[string]string{"text": "deploy shop"}, nil)
	var s2full struct {
		Session map[string]any
		Events  []map[string]any
	}
	for i := 0; i < 100; i++ {
		c.must("GET", "/api/workspaces/acme/sessions/"+sid2, nil, &s2full)
		if s2full.Session["state"] == "idle" && len(s2full.Events) > 3 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if _, err := os.Stat(filepath.Join(dest, "Dockerfile")); err != nil {
		t.Fatalf("repo not cloned: %v", s2full.Events)
	}
	results := 0
	for _, ev := range s2full.Events {
		if ev["type"] == "approval_request" {
			t.Fatal("auto mode on dev should not ask")
		}
		if ev["type"] == "tool_result" {
			results++
			out := ev["output"].(string)
			if ev["isError"] == true || (!strings.Contains(out, "saved") && !strings.Contains(out, "HEAD ")) {
				t.Fatalf("tool failed: %s", out)
			}
		}
		b, _ := json.Marshal(ev)
		if strings.Contains(string(b), "SuperSecretToken") {
			t.Fatal("git token leaked into events")
		}
	}
	if results != 7 {
		t.Fatalf("expected clone, update, report, memory, 3 artifacts: got %d results", results)
	}
	var arts []map[string]any
	c.must("GET", "/api/workspaces/acme/artifacts", nil, &arts)
	paths := []string{}
	for _, a := range arts {
		paths = append(paths, a["path"].(string))
	}
	joined := strings.Join(paths, "\n")
	for _, want := range []string{"artifacts/INDEX.md", "/app-1/" + strings.TrimPrefix(filepath.ToSlash(dest), "/") + "/Dockerfile", "/app-1/leak.txt", "/escape.csv"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("artifact %s missing in:\n%s", want, joined)
		}
	}
	for _, p := range paths {
		if strings.Contains(p, "..") {
			t.Fatalf("unsafe artifact path %s", p)
		}
		var fc map[string]any
		c.must("GET", "/api/workspaces/acme/artifacts/file?path="+p, nil, &fc)
		if strings.Contains(fc["content"].(string), "S3cr3t-P4ssw0rd!") {
			t.Fatalf("secret saved unredacted in %s", p)
		}
	}
	if idx, _ := os.ReadFile(filepath.Join(home, "data", "workspaces", "acme", "artifacts", "INDEX.md")); !strings.Contains(string(idx), "image base") || !strings.Contains(string(idx), sid2) {
		t.Fatalf("index: %s", idx)
	}
	// Everything the agent produced lives in the workspace folder.
	wsDir := filepath.Join(home, "data", "workspaces", "acme")
	reports, _ := filepath.Glob(filepath.Join(wsDir, "reports", "*", "*", "*-shop-deployment-on-app-1.md"))
	if len(reports) != 1 {
		t.Fatalf("report not written: %v", reports)
	}
	if b, _ := os.ReadFile(reports[0]); !strings.Contains(string(b), "session: "+sid2) || !strings.Contains(string(b), "Cloned shop") {
		t.Fatalf("report content: %s", b)
	}
	if b, _ := os.ReadFile(filepath.Join(wsDir, "MEMORY.md")); !strings.Contains(string(b), "GitRemote/shop") {
		t.Fatalf("memory not in workspace: %s", b)
	}
	for _, p := range []string{".opsdeck/sessions/" + sid2 + ".json", ".opsdeck/known_hosts"} {
		if _, err := os.Stat(filepath.Join(wsDir, p)); err != nil {
			t.Fatalf("%s not in workspace folder: %v", p, err)
		}
	}
	var docsList []map[string]any
	c.must("GET", "/api/workspaces/acme/docs", nil, &docsList)
	foundReport := false
	for _, d := range docsList {
		if strings.HasPrefix(d["path"].(string), "reports/") && d["title"] == "Shop deployment on app-1" {
			foundReport = true
		}
	}
	if !foundReport {
		t.Fatalf("report not listed in docs: %v", docsList)
	}
	if cfg, _ := os.ReadFile(filepath.Join(dest, ".git", "config")); strings.Contains(string(cfg), "SuperSecret") {
		t.Fatal("token persisted in .git/config")
	}

	// --- an external agent (fake Claude Code) working through the MCP bridge
	settings["providers"] = []map[string]any{{"id": "cc", "type": "claude-code", "name": "Claude Code", "command": os.Args[0],
		"env": map[string]string{"OPSDECK_FAKE_CLAUDE": "1"}, "models": []string{"default"}}}
	c.must("PUT", "/api/settings", settings, nil)
	var s3 map[string]any
	c.must("POST", "/api/workspaces/acme/sessions", map[string]any{"model": "cc/default", "mode": "ask"}, &s3)
	sid3 := s3["id"].(string)
	marker := filepath.Join(workdir, "from-claude")
	c.must("POST", "/api/workspaces/acme/sessions/"+sid3+"/messages", map[string]string{"text": "run app-1 echo via-bridge\nrun app-1 touch " + marker}, nil)
	var s3full struct {
		Session map[string]any
		Events  []map[string]any
	}
	approvedExt := false
	for i := 0; i < 200; i++ {
		c.must("GET", "/api/workspaces/acme/sessions/"+sid3, nil, &s3full)
		for _, ev := range s3full.Events {
			if ev["type"] == "approval_request" && !approvedExt {
				approvedExt = true
				c.must("POST", "/api/workspaces/acme/sessions/"+sid3+"/approvals/"+ev["approvalId"].(string), map[string]string{"decision": "approve"}, nil)
			}
		}
		if s3full.Session["state"] == "idle" && len(s3full.Events) > 2 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !approvedExt {
		t.Fatalf("external agent's change did not ask for approval: %v", s3full.Events)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("approved command from the external agent did not run: %v", s3full.Events)
	}
	reply := ""
	for _, ev := range s3full.Events {
		if ev["type"] == "assistant" {
			reply = ev["text"].(string)
		}
		if ev["type"] == "error" {
			t.Fatalf("external agent error: %v", ev["message"])
		}
	}
	if !strings.Contains(reply, "via-bridge") || !strings.HasPrefix(fmt.Sprint(s3full.Session["external"]), "cc:") {
		t.Fatalf("external reply/session: %q %v", reply, s3full.Session["external"])
	}
	// The bridge token only works while a turn runs.
	if res, err := http.Post(ts.URL+"/mcp/"+strings.Repeat("a", 48), "application/json", strings.NewReader("{}")); err != nil || res.StatusCode != 401 {
		t.Fatalf("bridge without a valid token must be refused: %v %v", err, res.StatusCode)
	}

	// --- audit
	var entries []map[string]any
	c.must("GET", "/api/audit?workspace=acme", nil, &entries)
	actions := map[string]bool{}
	for _, e := range entries {
		actions[fmt.Sprint(e["action"])+"/"+fmt.Sprint(e["result"])] = true
	}
	for _, want := range []string{"host.exec/ok", "tool.exec/ok", "tool.exec/denied", "resource.create/ok"} {
		if !actions[want] {
			t.Errorf("audit missing %s (have %v)", want, actions)
		}
	}
	var verify map[string]any
	c.must("GET", "/api/audit/verify", nil, &verify)
	if verify["ok"] != true {
		t.Fatalf("audit chain broken: %v", verify)
	}

	// --- lock
	c.must("POST", "/api/vault/lock", nil, nil)
	if code := c.do("GET", "/api/vault/items?scope=acme", nil, &st); code != 423 {
		t.Fatalf("expected 423 when locked, got %d", code)
	}
	if code := c.do("POST", "/api/vault/unlock", map[string]string{"password": "wrong"}, &st); code != 401 {
		t.Fatalf("wrong password: %d", code)
	}
	c.must("POST", "/api/vault/unlock", map[string]string{"password": "correct horse battery"}, nil)
}
