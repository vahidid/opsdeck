//go:build windows

package vpn

import (
	"context"
	"os/exec"

	"opsdeck/internal/sysproc"
)

func setProcAttrs(cmd *exec.Cmd, elevated bool) {}

func interrupt(cmd *exec.Cmd) {
	if cmd.Process != nil {
		cmd.Process.Kill()
	}
}

func kill(cmd *exec.Cmd) {
	if cmd.Process != nil {
		cmd.Process.Kill()
	}
}

func hasNetAdmin() bool { return false }

func shellCommand(ctx context.Context, command string) *exec.Cmd {
	return sysproc.CommandContext(ctx, "cmd", "/C", command)
}
