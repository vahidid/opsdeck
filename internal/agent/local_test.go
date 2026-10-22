package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"opsdeck/internal/workspace"
)

func TestLocalPolicy(t *testing.T) {
	home := t.TempDir()
	state := filepath.Join(home, ".local", "state", "opsdeck")
	ws := filepath.Join(home, "work", "acme")
	os.MkdirAll(filepath.Join(ws, ".opsdeck"), 0o700)
	os.WriteFile(filepath.Join(ws, "workspace.yaml"), []byte("kind: Workspace\n"), 0o600)
	os.MkdirAll(state, 0o700)
	os.WriteFile(filepath.Join(state, "server.token"), []byte("t"), 0o600)
	os.MkdirAll(filepath.Join(home, "projects"), 0o700)
	os.Symlink(state, filepath.Join(home, "projects", "sneaky"))
	p := &LocalPolicy{Home: home, Forbidden: []string{state}, AllowedDirs: []string{filepath.Join(home, "projects")}}

	for _, bad := range []string{"~/.local/state/opsdeck/server.token", "~/projects/sneaky/server.token", "~/work/acme/.opsdeck/known_hosts", "relative/path"} {
		if _, err := p.Resolve(bad); err == nil {
			t.Errorf("%s should be refused", bad)
		}
	}
	// a link to a credential file is a credential file
	os.MkdirAll(filepath.Join(home, ".ssh"), 0o700)
	os.WriteFile(filepath.Join(home, ".ssh", "config"), []byte("Host *\n"), 0o600)
	os.Symlink(filepath.Join(home, ".ssh", "config"), filepath.Join(home, "projects", "sshcfg"))
	if !p.Sensitive(filepath.Join(home, "projects", "sshcfg")) {
		t.Error("symlink to ~/.ssh/config must be sensitive")
	}
	got, err := p.Resolve("~/projects/../projects/app/compose.yml")
	if err != nil || got != filepath.Join(home, "projects", "app", "compose.yml") {
		t.Fatalf("resolve: %q %v", got, err)
	}
	if !p.Sensitive(filepath.Join(home, ".ssh", "config")) || !p.Sensitive("/srv/x/server.key") || p.Sensitive(filepath.Join(home, "projects", "a.yml")) {
		t.Error("sensitive detection")
	}

	env := &Env{WS: &workspace.Workspace{Path: ws}, Local: p}
	w := &workspace.Workspace{}
	decide := func(c *Call, granted map[string]bool) string {
		return Decide(ModeAsk, w, "local_read_file", c, granted, nil, Policy{}).Effect
	}

	// allowed folder and workspace: no question
	c := &Call{Risk: RiskRead}
	p.readAccess(c, env, filepath.Join(home, "projects", "app", "a.yml"), false)
	if decide(c, nil) != "allow" {
		t.Error("allowed folder should not ask")
	}
	c = &Call{Risk: RiskRead}
	p.readAccess(c, env, filepath.Join(ws, "docs", "x.md"), false)
	if decide(c, nil) != "allow" {
		t.Error("workspace folder should not ask")
	}
	// elsewhere: ask, and a grant on a parent folder covers sub-folders
	c = &Call{Risk: RiskRead}
	p.readAccess(c, env, "/etc/nginx/sites-enabled/default", false)
	if decide(c, nil) != "ask" {
		t.Error("outside read should ask")
	}
	if decide(c, map[string]bool{"local-read:/etc|read": true}) != "allow" {
		t.Error("parent folder grant should cover the file")
	}
	// credential files: per-file consent even inside an allowed folder
	c = &Call{Risk: RiskRead}
	p.readAccess(c, env, filepath.Join(home, "projects", "app", ".env"), false)
	if decide(c, map[string]bool{"local-read:" + filepath.Join(home, "projects", "app") + "|read": true}) != "ask" {
		t.Error("credential file must ask per file")
	}
	// writes ask in ask mode, until allowed for the folder
	c = &Call{Risk: RiskChange}
	p.writeAccess(c, filepath.Join(home, "projects", "app", "compose.yml"))
	if decide(c, nil) != "ask" {
		t.Error("local write should ask in ask mode")
	}
	if decide(c, map[string]bool{"local-write:" + filepath.Join(home, "projects") + "|change": true}) != "allow" {
		t.Error("folder write grant")
	}
	if Decide(ModePlan, w, "local_write_file", c, nil, nil, Policy{}).Effect != "deny" {
		t.Error("plan mode must deny writes")
	}
}

func TestModes(t *testing.T) {
	w := &workspace.Workspace{}
	type call struct {
		name string
		c    Call
	}
	calls := []call{
		{"host read", Call{Risk: RiskRead, Target: "host/app-1", Environment: "prod"}},
		{"outside read", Call{Risk: RiskRead, Target: "local:/etc/hosts", Environment: "local", Consent: "outside"}},
		{"credential read", Call{Risk: RiskRead, Target: "local:/home/u/.ssh/config", Environment: "local", Consent: "cred", Sensitive: true}},
		{"local write", Call{Risk: RiskChange, Target: "local:/tmp/x", Environment: "local", Protected: true}},
		{"local command", Call{Risk: RiskChange, Target: "local", Environment: "local", Protected: true}},
		{"local rm -rf", Call{Risk: RiskDestructive, Target: "local", Environment: "local", Protected: true}},
		{"vault item", Call{Risk: RiskChange, Target: "vault", Environment: "vault", Protected: true, Guarded: true}},
		{"working copy commit", Call{Risk: RiskChange, Target: "git/deploy", Environment: "working copy"}},
		{"host change dev", Call{Risk: RiskChange, Target: "host/app-1", Environment: "dev"}},
		{"host change prod", Call{Risk: RiskChange, Target: "host/app-1", Environment: "prod", Protected: true}},
		{"host destructive", Call{Risk: RiskDestructive, Target: "host/app-1", Environment: "dev"}},
		{"git push", Call{Risk: RiskChange, Target: "git/deploy", Environment: "prod"}},
	}
	want := map[string]map[string]string{
		// mode/policy → call → effect
		"plan": {"host read": "allow", "outside read": "ask", "credential read": "ask", "local write": "deny", "local command": "deny", "local rm -rf": "deny", "vault item": "deny",
			"working copy commit": "deny", "host change dev": "deny", "host change prod": "deny", "host destructive": "deny", "git push": "deny"},
		"ask": {"host read": "allow", "outside read": "ask", "credential read": "ask", "local write": "ask", "local command": "ask", "local rm -rf": "ask", "vault item": "ask",
			"working copy commit": "ask", "host change dev": "ask", "host change prod": "ask", "host destructive": "ask", "git push": "ask"},
		"auto": {"host read": "allow", "outside read": "allow", "credential read": "ask", "local write": "allow", "local command": "allow", "local rm -rf": "ask", "vault item": "ask",
			"working copy commit": "allow", "host change dev": "ask", "host change prod": "ask", "host destructive": "ask", "git push": "ask"},
		"auto+servers": {"host read": "allow", "outside read": "allow", "credential read": "ask", "local write": "allow", "local command": "allow", "local rm -rf": "ask", "vault item": "ask",
			"working copy commit": "allow", "host change dev": "allow", "host change prod": "ask", "host destructive": "ask", "git push": "allow"},
		"full": {"host read": "allow", "outside read": "allow", "credential read": "allow", "local write": "allow", "local command": "allow", "local rm -rf": "allow", "vault item": "allow",
			"working copy commit": "allow", "host change dev": "ask", "host change prod": "ask", "host destructive": "ask", "git push": "ask"},
		"full+servers": {"host read": "allow", "outside read": "allow", "credential read": "allow", "local write": "allow", "local command": "allow", "local rm -rf": "allow", "vault item": "allow",
			"working copy commit": "allow", "host change dev": "allow", "host change prod": "allow", "host destructive": "allow", "git push": "allow"},
	}
	for key, exp := range want {
		mode, servers, _ := strings.Cut(key, "+")
		for _, x := range calls {
			c := x.c
			if got := Decide(mode, w, "t", &c, nil, nil, Policy{AutoServerChanges: servers != ""}).Effect; got != exp[x.name] {
				t.Errorf("%s: %s = %s, want %s", key, x.name, got, exp[x.name])
			}
		}
	}
	// workspace rules still win, in every mode
	deny := &workspace.Workspace{Permissions: workspace.Permissions{Rules: []workspace.Rule{{Effect: "deny", Risk: RiskDestructive}}}}
	c := Call{Risk: RiskDestructive, Target: "local", Environment: "local"}
	if Decide(ModeFull, deny, "local_exec", &c, nil, nil, Policy{AutoServerChanges: true}).Effect != "deny" {
		t.Error("a workspace deny rule must hold in full mode")
	}
}

func TestNumberLines(t *testing.T) {
	out := numberLines("f", "a\nb\nc\n", false, 2, 1)
	if !strings.Contains(out, "lines 2-2 of 3") || !strings.Contains(out, "     2\tb\n") || strings.Contains(out, "\tc") {
		t.Fatalf("%q", out)
	}
}
