//go:build desktop && !windows

package main

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// A PATH entry that only .bashrc adds, behind the interactive-shell guard
// of Debian's and Ubuntu's .bashrc, reaches the app; the session's stay.
func TestLoginShellEnv(t *testing.T) {
	if _, err := os.Stat("/bin/bash"); err != nil {
		t.Skip("no bash")
	}
	home := t.TempDir()
	os.WriteFile(filepath.Join(home, ".bash_profile"), []byte("[ -f ~/.bashrc ] && . ~/.bashrc\n"), 0o644)
	os.WriteFile(filepath.Join(home, ".bashrc"), []byte("case $- in *i*) ;; *) return;; esac\nexport PATH=/opt/from-bashrc:$PATH\n"), 0o644)
	os.MkdirAll(filepath.Join(home, ".local", "bin"), 0o755)
	t.Setenv("HOME", home)
	t.Setenv("SHELL", "/bin/bash")
	t.Setenv("PATH", "/usr/bin:/bin:/session/only")
	loginShellEnv()
	dirs := filepath.SplitList(os.Getenv("PATH"))
	for _, want := range []string{"/opt/from-bashrc", "/session/only", "/usr/bin", filepath.Join(home, ".local", "bin")} {
		if !slices.Contains(dirs, want) {
			t.Errorf("PATH %q lacks %s", dirs, want)
		}
	}
	if len(dirs) != len(uniqueDirs(dirs)) {
		t.Errorf("PATH has repeated entries: %q", dirs)
	}
}
