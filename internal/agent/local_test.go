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
		return Decide(ModeAuto, w, "local_read_file", c, granted, nil).Effect
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
	// writes are protected: ask even in auto mode, until allowed for the folder
	c = &Call{Risk: RiskChange}
	p.writeAccess(c, filepath.Join(home, "projects", "app", "compose.yml"))
	if decide(c, nil) != "ask" {
		t.Error("local write should ask in auto mode")
	}
	if decide(c, map[string]bool{"local-write:" + filepath.Join(home, "projects") + "|change": true}) != "allow" {
		t.Error("folder write grant")
	}
	if Decide(ModePlan, w, "local_write_file", c, nil, nil).Effect != "deny" {
		t.Error("plan mode must deny writes")
	}
}

func TestNumberLines(t *testing.T) {
	out := numberLines("f", "a\nb\nc\n", false, 2, 1)
	if !strings.Contains(out, "lines 2-2 of 3") || !strings.Contains(out, "     2\tb\n") || strings.Contains(out, "\tc") {
		t.Fatalf("%q", out)
	}
}
