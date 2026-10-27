//go:build desktop && !windows

package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// loginShellEnv takes PATH from the user's login shell. Apps started from
// the Finder, the Dock or a Linux desktop's menu get the PATH of the
// session, not the one your shell profile builds (Homebrew, ~/.local/bin,
// nvm, ~/go/bin…), so git, kubectl plugins, ansible, VPN clients or agent
// CLIs such as claude would not be found. The session's own entries stay,
// after the shell's.
func loginShellEnv() {
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/sh"
		if runtime.GOOS == "darwin" {
			shell = "/bin/zsh"
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	const mark = "__OPSDECK_PATH__"
	var dirs []string
	// -i too: many profiles (nvm, ~/.local/bin on Debian and Ubuntu) set PATH
	// in .bashrc or .zshrc only
	if out, err := exec.CommandContext(ctx, shell, "-ilc", `printf '`+mark+`%s`+mark+`' "$PATH"`).Output(); err == nil {
		if parts := strings.Split(string(out), mark); len(parts) >= 3 {
			dirs = filepath.SplitList(parts[1])
		}
	}
	dirs = append(dirs, filepath.SplitList(os.Getenv("PATH"))...)
	if home, err := os.UserHomeDir(); err == nil {
		// where Claude Code, pipx and others install, when no profile adds it
		for _, d := range []string{filepath.Join(home, ".local", "bin"), filepath.Join(home, "bin")} {
			if fi, err := os.Stat(d); err == nil && fi.IsDir() {
				dirs = append(dirs, d)
			}
		}
	}
	os.Setenv("PATH", strings.Join(uniqueDirs(dirs), string(os.PathListSeparator)))
}

// uniqueDirs drops empty and repeated entries, keeping the first.
func uniqueDirs(dirs []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, d := range dirs {
		d = strings.TrimSpace(d)
		if d == "" || seen[d] {
			continue
		}
		seen[d] = true
		out = append(out, d)
	}
	return out
}
