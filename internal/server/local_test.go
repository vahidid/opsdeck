package server

import (
	"bufio"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"encoding/pem"
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
	"golang.org/x/crypto/ssh/knownhosts"

	"opsdeck/internal/paths"
	"opsdeck/internal/testutil/fakellm"
	"opsdeck/internal/testutil/sshd"
)

// TestLocalHarness: the agent works with the user's own computer. It reads
// ~/.ssh/known_hosts (with consent), imports the user's SSH key into the vault
// without ever seeing it, pins the host keys the user already trusts, points
// a host at the imported key and connects; opsdeck's own files stay out of
// reach and local writes and commands need approval.
func TestLocalHarness(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("OPSDECK_HOME", t.TempDir())
	dirs := paths.Default()
	srv, err := New(dirs, Options{Port: 0})
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

	// the user's key and the server they already use
	pubRaw, privRaw, _ := ed25519.GenerateKey(rand.Reader)
	block, _ := ssh.MarshalPrivateKey(privRaw, "me@laptop")
	keyPEM := pem.EncodeToMemory(block)
	userPub, _ := ssh.NewPublicKey(pubRaw)
	app, err := sshd.Start("127.0.0.1:0", sshd.Options{AuthorizedKeys: []ssh.PublicKey{userPub}, Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	_, aport, _ := net.SplitHostPort(app.Addr)
	port, _ := strconv.Atoi(aport)
	os.MkdirAll(filepath.Join(home, ".ssh"), 0o700)
	os.WriteFile(filepath.Join(home, ".ssh", "id_ed25519"), keyPEM, 0o600)
	os.WriteFile(filepath.Join(home, ".ssh", "id_ed25519.pub"), ssh.MarshalAuthorizedKey(userPub), 0o644)
	hashed := knownhosts.HashHostname(knownhosts.Normalize(app.Addr))
	os.WriteFile(filepath.Join(home, ".ssh", "known_hosts"), []byte(
		"github.com ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOMqqnkVzrm0SdG6UOoqKLsabgH5C9okWi0dh2l9GKJl\n"+
			knownhosts.Line([]string{hashed}, app.HostKey)+"\n"), 0o600)
	c.must("POST", "/api/workspaces/acme/resources", map[string]any{"kind": "Host", "name": "app-1", "environment": "dev",
		"spec": map[string]any{"address": "127.0.0.1", "port": port}}, nil)

	out := filepath.Join(home, "projects", "shop", "compose.yml")
	token := filepath.Join(dirs.State, "server.token")
	fake := fakellm.New([]fakellm.Turn{
		{Tools: []fakellm.ToolCall{{Name: "local_read_file", Args: map[string]any{"path": "~/.ssh/known_hosts"}}}},
		{Tools: []fakellm.ToolCall{{Name: "local_read_file", Args: map[string]any{"path": "~/.ssh/known_hosts"}}}},
		{Tools: []fakellm.ToolCall{{Name: "local_read_file", Args: map[string]any{"path": "~/.ssh/id_ed25519"}}}},
		{Tools: []fakellm.ToolCall{{Name: "vault_import_ssh_key", Args: map[string]any{"path": "~/.ssh/id_ed25519", "username": "deploy"}}}},
		{Tools: []fakellm.ToolCall{{Name: "known_hosts_import", Args: map[string]any{}}}},
		{Tools: []fakellm.ToolCall{{Name: "workspace_resource", Args: map[string]any{"action": "apply", "resource": map[string]any{
			"kind": "Host", "name": "app-1", "environment": "dev", "spec": map[string]any{"address": "127.0.0.1", "port": port, "auth": "vault://acme/id-ed25519"}}}}}},
		{Tools: []fakellm.ToolCall{{Name: "exec", Args: map[string]any{"target": "app-1", "command": "echo connected-ok"}}}},
		{Tools: []fakellm.ToolCall{{Name: "local_read_file", Args: map[string]any{"path": token}}}},
		{Tools: []fakellm.ToolCall{{Name: "local_list_dir", Args: map[string]any{"path": "~", "hidden": true}}}},
		{Tools: []fakellm.ToolCall{{Name: "local_exec", Args: map[string]any{"command": "echo local-ok"}}}},
		{Tools: []fakellm.ToolCall{{Name: "local_write_file", Args: map[string]any{"path": "~/projects/shop/compose.yml", "content": "services: {}\n"}}}},
		{Tools: []fakellm.ToolCall{{Name: "vault_list", Args: map[string]any{}}}},
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

	streamRes, err := c.http.Get(ts.URL + "/api/workspaces/acme/sessions/" + sid + "/stream")
	if err != nil {
		t.Fatal(err)
	}
	defer streamRes.Body.Close()
	c.must("POST", "/api/workspaces/acme/sessions/"+sid+"/messages", map[string]string{"text": "look at my known_hosts and import my key into the vault"}, nil)

	var asked []string
	started := false
	results := map[string]string{}
	sc := bufio.NewScanner(streamRes.Body)
	sc.Buffer(make([]byte, 1<<20), 8<<20)
	deadline := time.Now().Add(60 * time.Second)
	for sc.Scan() && time.Now().Before(deadline) {
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
			decision := "approve"
			if tool == "local_read_file" && len(asked) == 1 {
				decision = "always"
				if !strings.Contains(ev["grantLabel"].(string), ".ssh/known_hosts") {
					t.Errorf("grant label: %v", ev["grantLabel"])
				}
			}
			c.must("POST", "/api/workspaces/acme/sessions/"+sid+"/approvals/"+ev["approvalId"].(string), map[string]string{"decision": decision}, nil)
		case "tool_result":
			results[ev["callId"].(string)], _ = ev["output"].(string)
		}
		if ev["type"] == "status" && ev["state"] == "running" {
			started = true
		}
		if started && ev["type"] == "status" && ev["state"] == "idle" {
			break
		}
	}

	want := []string{"local_read_file", "local_read_file", "vault_import_ssh_key", "known_hosts_import", // workspace_resource runs in auto mode
		"local_list_dir", "local_exec", "local_write_file"}
	if strings.Join(asked, ",") != strings.Join(want, ",") {
		t.Fatalf("approvals asked:\n got %v\nwant %v", asked, want)
	}
	check := func(id, sub string) {
		t.Helper()
		if !strings.Contains(results[id], sub) {
			t.Errorf("%s: want %q in %q", id, sub, results[id])
		}
	}
	check("call_0_0", "|1|")         // known_hosts content
	check("call_1_0", "github.com")  // second read: granted, no approval
	check("call_2_0", "never shown") // the private key file is refused
	check("call_3_0", "vault://acme/id-ed25519")
	check("call_4_0", "pinned 1 key")
	check("call_5_0", "updated Host/app-1")
	check("call_6_0", "connected-ok")
	check("call_7_0", "belongs to opsdeck")
	check("call_8_0", ".ssh")
	check("call_9_0", "local-ok")
	check("call_10_0", "written")
	check("call_11_0", "vault://acme/id-ed25519")
	if b, err := os.ReadFile(out); err != nil || string(b) != "services: {}\n" {
		t.Errorf("compose.yml not written: %v %q", err, b)
	}

	// the private key never reached the model
	keyBody := strings.Split(strings.TrimSpace(string(keyPEM)), "\n")[2]
	for _, req := range fake.Requests {
		b, _ := json.Marshal(req)
		if strings.Contains(string(b), keyBody) || strings.Contains(string(b), "BEGIN OPENSSH PRIVATE KEY") {
			t.Fatal("the private key reached the model")
		}
	}
	// it is in the vault, usable by the agent, with its public key
	var items []map[string]any
	c.must("GET", "/api/vault/items?scope=acme", nil, &items)
	if len(items) != 1 || items[0]["name"] != "id-ed25519" || items[0]["type"] != "ssh-key" {
		t.Fatalf("vault items: %v", items)
	}
	if !strings.Contains(fake.LastSystem(), "vault_import_ssh_key") {
		t.Error("system prompt should describe local access")
	}

	// with local access turned off the tools disappear
	settings["localAccess"] = map[string]any{"disabled": true}
	c.must("PUT", "/api/settings", settings, nil)
	if p := srv.localPolicy(); p != nil {
		t.Fatal("local access still on")
	}
}
