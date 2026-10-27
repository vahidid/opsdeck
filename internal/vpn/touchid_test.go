package vpn

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// runEnableTouchID runs the script that turns on Touch ID on a copy of
// sudo_local (ownership aside: that needs root and macOS's wheel group).
func runEnableTouchID(t *testing.T, path string) {
	script := strings.ReplaceAll(enableTouchIDScript, pamSudoLocal, path)
	script = strings.ReplaceAll(script, "chown root:wheel \"$f\"\n", "")
	if out, err := exec.Command("/bin/sh", "-c", script).CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
}

func TestEnableTouchIDScript(t *testing.T) {
	if _, err := exec.LookPath("/bin/sh"); err != nil {
		t.Skip("no sh")
	}
	dir := t.TempDir()

	// no sudo_local yet
	fresh := filepath.Join(dir, "fresh")
	runEnableTouchID(t, fresh)
	b, _ := os.ReadFile(fresh)
	if !pamTIDLine.Match(b) {
		t.Fatalf("pam_tid not on:\n%s", b)
	}
	runEnableTouchID(t, fresh) // read-only now, and already on: unchanged
	if b2, _ := os.ReadFile(fresh); string(b2) != string(b) {
		t.Fatalf("second run changed it:\n%s", b2)
	}

	// macOS's template (commented out) with pam_reattach on: pam_tid follows it
	kept := filepath.Join(dir, "kept")
	os.WriteFile(kept, []byte("# sudo_local\n#auth       sufficient     pam_tid.so\nauth optional /opt/homebrew/lib/pam/pam_reattach.so\n"), 0o644)
	runEnableTouchID(t, kept)
	b, _ = os.ReadFile(kept)
	s := string(b)
	if !pamTIDLine.Match(b) || strings.Index(s, "pam_reattach") > strings.LastIndex(s, "\nauth       sufficient     pam_tid.so") {
		t.Fatalf("sudo_local:\n%s", s)
	}
	if pamTIDLine.MatchString("#auth       sufficient     pam_tid.so") {
		t.Fatal("a commented-out pam_tid counts as on")
	}
}

func TestIfaceWithAddr(t *testing.T) {
	if name := ifaceWithAddr("127.0.0.1"); name == "" {
		t.Fatal("no interface has 127.0.0.1")
	}
	if name := ifaceWithAddr("192.0.2.123"); name != "" {
		t.Fatalf("%s has a TEST-NET address", name)
	}
}
