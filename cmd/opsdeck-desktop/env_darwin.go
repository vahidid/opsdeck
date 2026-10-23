//go:build desktop && darwin

package main

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"time"
)

// loginShellEnv takes PATH from the user's login shell. Apps started from
// the Finder or the Dock get only /usr/bin:/bin:/usr/sbin:/sbin, so git,
// kubectl plugins, ansible, VPN clients or agent CLIs from Homebrew and
// ~/.local/bin would not be found.
func loginShellEnv() {
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/zsh"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	const mark = "__OPSDECK_PATH__"
	out, err := exec.CommandContext(ctx, shell, "-ilc", `printf '`+mark+`%s`+mark+`' "$PATH"`).Output()
	if err != nil {
		return
	}
	parts := strings.Split(string(out), mark)
	if len(parts) >= 3 && strings.TrimSpace(parts[1]) != "" {
		os.Setenv("PATH", parts[1])
	}
}
