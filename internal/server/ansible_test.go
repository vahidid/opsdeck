package server

import (
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

	"golang.org/x/crypto/ssh"

	"opsdeck/internal/paths"
	"opsdeck/internal/testutil/fakellm"
	"opsdeck/internal/testutil/sshd"
)

// TestAnsible drives the Ansible page's API and the agent's tools against
// real ansible and two SSH servers (a bastion and a host reached through
// it), with vault passwords typed into the approval card.
func TestAnsible(t *testing.T) {
	for _, prog := range []string{"ansible-playbook", "ssh"} {
		if _, err := exec.LookPath(prog); err != nil {
			t.Skip(prog + " not installed")
		}
	}
	home := t.TempDir()
	t.Setenv("OPSDECK_HOME", home)
	srv, err := New(paths.Default(), Options{Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	srv.ansible.Executable = os.Args[0] // the test binary acts as `opsdeck socks-connect`
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	srv.agents.SetMCPBase(ts.URL + "/mcp/")
	jar, _ := cookiejar.New(nil)
	c := &client{t: t, base: ts.URL, http: &http.Client{Jar: jar, Timeout: 3 * time.Minute}}
	c.must("POST", "/api/auth/login", map[string]string{"token": srv.token}, nil)
	c.must("POST", "/api/vault/init", map[string]string{"password": "correct horse battery"}, nil)
	c.must("POST", "/api/workspaces", map[string]string{"name": "acme", "title": "ACME"}, nil)
	var key map[string]any
	c.must("POST", "/api/vault/ssh/generate", map[string]any{"scope": "acme", "name": "deploy-key", "algorithm": "ed25519"}, &key)
	pub, _, _, _, _ := ssh.ParseAuthorizedKey([]byte(key["publicKey"].(string)))

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
	port := func(s *sshd.Server) int { _, p, _ := net.SplitHostPort(s.Addr); n, _ := strconv.Atoi(p); return n }
	c.must("POST", "/api/workspaces/acme/resources", map[string]any{"kind": "Host", "name": "bastion",
		"spec": map[string]any{"address": "127.0.0.1", "port": port(bastion), "auth": "vault://acme/deploy-key"}}, nil)
	c.must("POST", "/api/workspaces/acme/resources", map[string]any{"kind": "Host", "name": "app-1", "environment": "dev", "via": "host/bastion",
		"spec": map[string]any{"address": "127.0.0.1", "port": port(app), "auth": "vault://acme/deploy-key", "roles": []string{"web"}}}, nil)

	// --- a new project with a starter layout
	c.must("POST", "/api/workspaces/acme/ansible/projects", map[string]any{"name": "infra", "source": "folder", "scaffold": true, "inventory": "workspace"}, nil)
	root := filepath.Join(home, "data", "workspaces", "acme", "ansible", "infra")
	if _, err := os.Stat(filepath.Join(root, "ansible.cfg")); err != nil {
		t.Fatal("not scaffolded")
	}
	marker := filepath.Join(workdir, "deployed.txt")
	c.must("PUT", "/api/workspaces/acme/ansible/p/infra/file", map[string]any{"path": "deploy.yml", "content": `- hosts: web
  gather_facts: false
  vars_files: [vault.yml]
  tasks:
    - name: Write the marker
      ansible.builtin.copy:
        dest: ` + marker + `
        content: "{{ greeting }} on {{ inventory_hostname }}\n"
`}, nil)
	c.must("PUT", "/api/workspaces/acme/ansible/p/infra/file", map[string]any{"path": "vault.yml", "content": "greeting: hello-from-vault\n"}, nil)

	// --- encrypting asks for the password first
	var e map[string]any
	if code := c.do("POST", "/api/workspaces/acme/ansible/p/infra/vault/encrypt", map[string]any{"path": "vault.yml"}, &e); code != 409 || e["code"] != "ansible_password" || e["vaultId"] != "default" {
		t.Fatalf("encrypt without a password: %d %v", code, e)
	}
	c.must("POST", "/api/workspaces/acme/ansible/p/infra/unlock", map[string]any{"vaultId": "default", "password": "open-sesame"}, nil)
	c.must("POST", "/api/workspaces/acme/ansible/p/infra/vault/encrypt", map[string]any{"path": "vault.yml"}, nil)
	var fc map[string]any
	c.must("GET", "/api/workspaces/acme/ansible/p/infra/file?path=vault.yml", nil, &fc)
	if fc["encrypted"] != true || strings.Contains(fc["text"].(string), "hello") {
		t.Fatalf("not encrypted: %v", fc)
	}
	if code := c.do("PUT", "/api/workspaces/acme/ansible/p/infra/file", map[string]any{"path": "vault.yml", "content": "plain: text\n"}, nil); code != 409 {
		t.Fatalf("plain text written over a vault file: %d", code)
	}
	c.must("DELETE", "/api/workspaces/acme/ansible/p/infra/unlock", nil, nil)
	if code := c.do("POST", "/api/workspaces/acme/ansible/p/infra/vault/view", map[string]any{"path": "vault.yml"}, nil); code != 409 {
		t.Fatalf("viewed after lock: %d", code)
	}
	if code := c.do("POST", "/api/workspaces/acme/ansible/p/infra/vault/view", map[string]any{"path": "vault.yml", "password": "nope"}, &e); code != 422 {
		t.Fatalf("wrong password: %d %v", code, e)
	}
	var view map[string]string
	c.must("POST", "/api/workspaces/acme/ansible/p/infra/vault/view", map[string]any{"path": "vault.yml", "password": "open-sesame"}, &view)
	if view["text"] != "greeting: hello-from-vault\n" {
		t.Fatalf("view: %v", view)
	}
	var sum map[string]any
	c.must("GET", "/api/workspaces/acme/ansible/p/infra", nil, &sum)
	if sum["vaultFiles"] != 1.0 || len(sum["unlocked"].([]any)) != 0 || len(sum["playbooks"].([]any)) != 2 {
		t.Fatalf("summary: %v", sum)
	}

	// --- inventory generated from the workspace's hosts
	var inv map[string]any
	c.must("GET", "/api/workspaces/acme/ansible/p/infra/inventory?inventory=workspace", nil, &inv)
	if !strings.Contains(inv["graph"].(string), "app-1") || !strings.Contains(inv["generated"].(string), "ansible_host: 127.0.0.1") {
		t.Fatalf("inventory: %v", inv)
	}

	// --- a run from the page, through the bastion, with the password given once
	var run map[string]any
	if code := c.do("POST", "/api/workspaces/acme/ansible/p/infra/runs", map[string]any{"playbook": "deploy.yml"}, &run); code != 409 {
		t.Fatalf("run without a password: %d %v", code, run)
	}
	c.must("POST", "/api/workspaces/acme/ansible/p/infra/runs", map[string]any{"playbook": "deploy.yml", "password": "open-sesame"}, &run)
	id := run["id"].(string)
	res, err := c.http.Get(ts.URL + "/api/workspaces/acme/ansible/runs/" + id + "/log")
	if err != nil {
		t.Fatal(err)
	}
	stream, _ := io.ReadAll(res.Body)
	res.Body.Close()
	c.must("GET", "/api/workspaces/acme/ansible/runs/"+id, nil, &run)
	if run["status"] != "ok" || !strings.Contains(string(stream), "PLAY RECAP") || !strings.Contains(string(stream), "event: end") {
		t.Fatalf("run %v\n%s", run, stream)
	}
	if b, _ := os.ReadFile(marker); string(b) != "hello-from-vault on app-1\n" {
		t.Fatalf("marker %q", b)
	}
	if strings.Contains(string(stream), "open-sesame") || strings.Contains(run["command"].(string), "/tmp") {
		t.Fatal("password or private paths in the log")
	}
	os.Remove(marker)

	// --- the agent: viewing and running ask for the password on the approval card
	fake := fakellm.New([]fakellm.Turn{
		{Tools: []fakellm.ToolCall{{Name: "ansible_vault", Args: map[string]any{"action": "view", "path": "vault.yml"}}}},
		{Tools: []fakellm.ToolCall{{Name: "ansible_run", Args: map[string]any{"playbook": "deploy.yml", "limit": "app-1"}}}},
		{Tools: []fakellm.ToolCall{{Name: "ansible_vault", Args: map[string]any{"action": "set", "path": "vault.yml", "key": "db_password", "generate": true}}}},
		{Text: "Done."},
	})
	defer fake.Close()
	var settings map[string]any
	c.must("GET", "/api/settings", nil, &settings)
	settings["providers"] = []map[string]any{{"id": "fake", "type": "openai", "name": "Fake", "baseUrl": fake.URL, "models": []string{"m1"}}}
	c.must("PUT", "/api/settings", settings, nil)
	var sess map[string]any
	c.must("POST", "/api/workspaces/acme/sessions", map[string]any{"mode": "auto"}, &sess)
	sid := sess["id"].(string)
	c.must("POST", "/api/workspaces/acme/sessions/"+sid+"/messages", map[string]string{"text": "deploy to app-1"}, nil)
	var full struct {
		Session map[string]any
		Events  []map[string]any
	}
	answered := map[string]bool{}
	var cards []map[string]any
	for i := 0; i < 1200; i++ {
		c.must("GET", "/api/workspaces/acme/sessions/"+sid, nil, &full)
		for _, ev := range full.Events {
			aid, _ := ev["approvalId"].(string)
			if ev["type"] != "approval_request" || answered[aid] {
				continue
			}
			answered[aid] = true
			cards = append(cards, ev)
			url := "/api/workspaces/acme/sessions/" + sid + "/approvals/" + aid
			switch len(cards) {
			case 1: // view: a wrong password keeps the card open
				if ev["secret"] == nil || ev["tool"] != "ansible_vault" {
					t.Fatalf("view card without a password field: %v", ev)
				}
				if code := c.do("POST", url, map[string]string{"decision": "approve"}, nil); code != 422 {
					t.Fatalf("approved without the password: %d", code)
				}
				if code := c.do("POST", url, map[string]string{"decision": "approve", "secret": "nope"}, nil); code != 422 {
					t.Fatalf("wrong password accepted: %d", code)
				}
				c.must("POST", url, map[string]string{"decision": "approve", "secret": "open-sesame"}, nil)
			case 2: // the run: asked again (the first was for once), kept this time
				if ev["secret"] == nil || !strings.Contains(ev["preview"].(string), "app-1") {
					t.Fatalf("run card: %v", ev)
				}
				c.must("POST", url, map[string]string{"decision": "always", "secret": "open-sesame"}, nil)
			default: // set is a local file change: in auto mode it needs no card, the password is remembered
				t.Fatalf("unexpected approval card: %v", ev)
			}
		}
		if full.Session["state"] == "idle" && len(full.Events) > 8 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if len(cards) != 2 {
		t.Fatalf("approval cards: %d", len(cards))
	}
	results := fake.LastToolResults()
	all := strings.Join(results, "\n")
	if !strings.Contains(all, "greeting: hello-from-vault") || !strings.Contains(all, "PLAY RECAP") || !strings.Contains(all, "app-1: ok=1 changed=1") ||
		!strings.Contains(all, "random 24-character") {
		t.Fatalf("tool results:\n%s", all)
	}
	if b, _ := os.ReadFile(marker); string(b) != "hello-from-vault on app-1\n" {
		t.Fatalf("agent run marker %q", b)
	}
	c.must("POST", "/api/workspaces/acme/ansible/p/infra/vault/view", map[string]any{"path": "vault.yml"}, &view)
	if !strings.Contains(view["text"], "db_password: \"") {
		t.Fatalf("generated value not set: %v", view)
	}
	if strings.Contains(fake.LastSystem(), "open-sesame") || !strings.Contains(fake.LastSystem(), "## Ansible projects") {
		t.Fatal("system prompt")
	}
	// the password is nowhere on disk: transcript, audit
	for _, dir := range []string{filepath.Join(home, "data", "workspaces", "acme", ".opsdeck"), filepath.Join(home, "state")} {
		filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
			if err == nil && !info.IsDir() {
				if b, _ := os.ReadFile(p); strings.Contains(string(b), "open-sesame") {
					t.Errorf("password stored in %s", p)
				}
			}
			return nil
		})
	}
	var runs []map[string]any
	c.must("GET", "/api/workspaces/acme/ansible/runs?project=infra", nil, &runs)
	if len(runs) != 2 || !strings.HasPrefix(runs[0]["actor"].(string), "agent:") {
		t.Fatalf("runs: %v", runs)
	}
}
