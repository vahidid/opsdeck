package scripts

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const bootstrap = `#!/usr/bin/env bash
set -euo pipefail
echo "user=$DEPLOY_USER port=$SSH_PORT key=$AUTHORIZED_KEY host=$OPSDECK_HOST v=$OPSDECK_SCRIPT_VERSION"
`

func newScript() Script {
	return Script{Meta: Meta{Name: "server-bootstrap", Description: "Initial setup of a fresh server",
		When: []string{"new-host"},
		Params: []Param{
			{Name: "DEPLOY_USER", Default: "deploy"},
			{Name: "SSH_PORT", Type: "int", Default: "22"},
			{Name: "AUTHORIZED_KEY", Type: "secret", Required: true},
		}}, Body: bootstrap}
}

func TestSaveVersionsAndApproval(t *testing.T) {
	st := Store{Dir: t.TempDir(), Scope: ScopeUser}

	// the user's own script is approved as saved
	s, err := st.Save(newScript(), "user")
	if err != nil {
		t.Fatal(err)
	}
	if s.Version != 1 || s.Status != StatusApproved || s.CreatedBy != "user" || s.File != "script.sh" {
		t.Fatalf("first save: %+v", s.Meta)
	}

	// the agent changing only the description keeps the approval and version
	in := newScript()
	in.Description = "Initial setup of a fresh Ubuntu server"
	if s, err = st.Save(in, "agent"); err != nil {
		t.Fatal(err)
	}
	if s.Version != 1 || s.Status != StatusApproved {
		t.Fatalf("description edit: v%d %s", s.Version, s.Status)
	}

	// the agent changing what runs makes a new draft version, keeping v1
	in.Body = bootstrap + "echo extra\n"
	if s, err = st.Save(in, "agent"); err != nil {
		t.Fatal(err)
	}
	if s.Version != 2 || s.Status != StatusDraft || s.CreatedBy != "user" || s.UpdatedBy != "agent" {
		t.Fatalf("agent edit: %+v", s.Meta)
	}
	if len(s.Versions) != 1 || s.Versions[0] != 1 {
		t.Fatalf("versions: %v", s.Versions)
	}
	v1, err := st.Version("server-bootstrap", 1)
	if err != nil || v1.Body != bootstrap {
		t.Fatalf("v1: %v %q", err, v1.Body)
	}

	// approving needs the hash the user reviewed
	if _, err := st.Approve("server-bootstrap", "0000"); err == nil {
		t.Fatal("approved with a stale hash")
	}
	if s, err = st.Approve("server-bootstrap", s.Hash); err != nil || s.Status != StatusApproved {
		t.Fatalf("approve: %v %s", err, s.Status)
	}

	// a hand edit (or a teammate's commit) makes it a draft again, also when
	// it writes the new hash into the script's files
	os.WriteFile(filepath.Join(st.Dir, "server-bootstrap", "script.sh"), []byte(bootstrap+"rm -rf /tmp/x\n"), 0o755)
	if s, _ = st.Get("server-bootstrap"); s.Status != StatusDraft {
		t.Fatalf("edited on disk but still %s", s.Status)
	}
	meta, _ := os.ReadFile(filepath.Join(st.Dir, "server-bootstrap", "script.yaml"))
	if strings.Contains(string(meta), s.Hash) {
		t.Fatal("approval stored in the shared script file")
	}
	os.WriteFile(filepath.Join(st.Dir, "server-bootstrap", "script.yaml"), append(meta, []byte("approved: "+s.Hash+"\n")...), 0o644)
	if s, _ = st.Get("server-bootstrap"); s.Status != StatusDraft {
		t.Fatal("a forged approval in script.yaml was accepted")
	}

	// parameter defaults are part of what is approved
	s, _ = st.Save(newScript(), "user")
	in = newScript()
	in.Params[0].Default = "root"
	if s2, _ := st.Save(in, "agent"); s2.Hash == s.Hash || s2.Status != StatusDraft {
		t.Fatalf("param default change kept approval: %s", s2.Status)
	}
}

func TestValidate(t *testing.T) {
	bad := map[string]func(*Script){
		"name":           func(s *Script) { s.Name = "Bad Name" },
		"description":    func(s *Script) { s.Description = " " },
		"empty":          func(s *Script) { s.Body = "\n" },
		"param name":     func(s *Script) { s.Params[0].Name = "deploy-user" },
		"reserved param": func(s *Script) { s.Params[0].Name = "OPSDECK_HOST" },
		"secret default": func(s *Script) { s.Params[2].Default = "ssh-ed25519 AAAA" },
		"int default":    func(s *Script) { s.Params[1].Default = "twenty" },
		"type":           func(s *Script) { s.Params[0].Type = "list" },
		"os":             func(s *Script) { s.OS = []string{"ubuntu; rm -rf /"} },
		"interpreter":    func(s *Script) { s.Body = "#!/bin/bash $(id)\necho\n" },
		"file":           func(s *Script) { s.File = "../x.sh" },
		"risk":           func(s *Script) { s.Risk = "low" },
	}
	for name, mutate := range bad {
		s := newScript()
		mutate(&s)
		if err := Validate(&s); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	s := newScript()
	if err := Validate(&s); err != nil {
		t.Fatal(err)
	}
}

func TestResolve(t *testing.T) {
	s := newScript()
	if _, _, err := Resolve(&s, map[string]string{}); err == nil || !strings.Contains(err.Error(), "AUTHORIZED_KEY is required") {
		t.Fatalf("missing required: %v", err)
	}
	if _, _, err := Resolve(&s, map[string]string{"AUTHORIZED_KEY": "ssh-ed25519 AAAA"}); err == nil {
		t.Fatal("secret value accepted instead of a vault reference")
	}
	if _, _, err := Resolve(&s, map[string]string{"AUTHORIZED_KEY": "vault://acme/k", "TYPO": "x"}); err == nil || !strings.Contains(err.Error(), "TYPO") {
		t.Fatalf("unknown param: %v", err)
	}
	if _, _, err := Resolve(&s, map[string]string{"AUTHORIZED_KEY": "vault://acme/k", "SSH_PORT": "abc"}); err == nil {
		t.Fatal("bad int accepted")
	}
	vals, secrets, err := Resolve(&s, map[string]string{"AUTHORIZED_KEY": "vault://acme/k#public_key", "SSH_PORT": "2222"})
	if err != nil {
		t.Fatal(err)
	}
	if vals["DEPLOY_USER"] != "deploy" || vals["SSH_PORT"] != "2222" || secrets["AUTHORIZED_KEY"] != "vault://acme/k#public_key" {
		t.Fatalf("resolved: %v %v", vals, secrets)
	}
	if _, ok := vals["AUTHORIZED_KEY"]; ok {
		t.Fatal("secret among plain values")
	}
}

// runLocal runs the real remote program with sh, as the SSH server would.
func runLocal(t *testing.T, s *Script, env map[string]string, force bool) (string, string, int) {
	t.Helper()
	payload, err := Payload(s, env, force)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("/bin/sh", "-c", Command)
	cmd.Stdin = strings.NewReader(payload)
	cmd.Env = append(os.Environ(), "TMPDIR="+t.TempDir())
	var out, errb strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &errb
	err = cmd.Run()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return out.String(), errb.String(), code
}

func TestPayloadRuns(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not installed")
	}
	s := newScript()
	env := map[string]string{"DEPLOY_USER": "it's me", "SSH_PORT": "22", "AUTHORIZED_KEY": "ssh-ed25519 AAAA key\nsecond line",
		"OPSDECK_HOST": "app-1", "OPSDECK_SCRIPT_VERSION": "3"}
	out, stderr, code := runLocal(t, &s, env, false)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	if want := "user=it's me port=22 key=ssh-ed25519 AAAA key\nsecond line host=app-1 v=3\n"; out != want {
		t.Fatalf("got %q want %q", out, want)
	}

	// the script's exit code comes back; the temporary folder is removed
	tmp := t.TempDir()
	s.Body = "#!/bin/sh\necho \"$0\" > " + tmp + "/path\nexit 7\n"
	if _, _, code := runLocal(t, &s, env, false); code != 7 {
		t.Fatalf("exit code %d", code)
	}
	b, _ := os.ReadFile(filepath.Join(tmp, "path"))
	if dir := filepath.Dir(strings.TrimSpace(string(b))); dir == "." || dirExists(dir) {
		t.Fatalf("temporary folder %q left behind", dir)
	}

	// a passing check skips the run unless forced
	s.Body = "echo ran\n"
	s.Check = `test "$DEPLOY_USER" = "it's me"`
	if out, _, _ := runLocal(t, &s, env, false); !strings.HasPrefix(out, markerApplied) {
		t.Fatalf("check did not skip: %q", out)
	}
	if out, _, _ := runLocal(t, &s, env, true); out != "ran\n" {
		t.Fatalf("force: %q", out)
	}
	s.Check = "false"
	if out, _, _ := runLocal(t, &s, env, false); out != "ran\n" {
		t.Fatalf("failing check: %q", out)
	}

	// unsupported systems stop before running
	s.Check = ""
	s.OS = []string{"plan9-4"}
	if out, stderr, code := runLocal(t, &s, env, false); code != ExitUnsupported || out != "" || !strings.Contains(stderr, "supports plan9-4") {
		t.Fatalf("os check: %d %q %q", code, out, stderr)
	}

	// a body containing a heredoc-looking line is kept as is
	s.OS = nil
	s.Body = "cat <<'EOF'\nOPSDECK_EOF_x\nEOF\n"
	if out, _, _ := runLocal(t, &s, env, false); out != "OPSDECK_EOF_x\n" {
		t.Fatalf("heredoc: %q", out)
	}
}

func dirExists(p string) bool {
	info, err := os.Stat(p)
	return err == nil && info.IsDir()
}

func TestMergeAndCopy(t *testing.T) {
	user := Store{Dir: t.TempDir(), Scope: ScopeUser}
	ws := Store{Dir: t.TempDir(), Scope: ScopeWorkspace, Approvals: filepath.Join(t.TempDir(), "approvals.json")}
	if _, err := user.Save(newScript(), "user"); err != nil {
		t.Fatal(err)
	}
	other := newScript()
	other.Name, other.Params = "harden", nil
	user.Save(other, "user")
	if err := Copy(user, "server-bootstrap", ws); err != nil {
		t.Fatal(err)
	}
	if err := Copy(user, "server-bootstrap", ws); err == nil {
		t.Fatal("copy over an existing script")
	}
	got, err := ws.Get("server-bootstrap")
	if err != nil || got.Status != StatusApproved || got.Scope != ScopeWorkspace {
		t.Fatalf("copied: %v %+v", err, got)
	}
	all := Merge(user.List(), ws.List())
	if len(all) != 2 || all[0].Name != "harden" || all[1].Scope != ScopeWorkspace || !all[1].Overrides {
		t.Fatalf("merge: %+v", all)
	}
	// a teammate with the same workspace files but not this user's approvals
	mate := Store{Dir: ws.Dir, Scope: ScopeWorkspace, Approvals: filepath.Join(t.TempDir(), "approvals.json")}
	if got, _ := mate.Get("server-bootstrap"); got.Status != StatusDraft {
		t.Fatalf("teammate sees %s without approving", got.Status)
	}
	if err := ws.Delete("server-bootstrap"); err != nil {
		t.Fatal(err)
	}
	if ws.approvals()["server-bootstrap"] != "" {
		t.Fatal("approval kept after delete")
	}
	if _, err := ws.Get("server-bootstrap"); err == nil {
		t.Fatal("not deleted")
	}
}
