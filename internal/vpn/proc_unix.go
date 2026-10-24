//go:build !windows

package vpn

import (
	"context"
	"os/exec"
	"syscall"

	"opsdeck/internal/sysproc"
)

func setProcAttrs(cmd *exec.Cmd, elevated bool) {
	cmd.SysProcAttr = sysProcAttr(elevated)
}

// interrupt sends SIGINT to the client (or to sudo, which relays it).
func interrupt(cmd *exec.Cmd) {
	if cmd.Process != nil {
		cmd.Process.Signal(syscall.SIGINT)
	}
}

func kill(cmd *exec.Cmd) {
	if cmd.Process != nil {
		cmd.Process.Signal(syscall.SIGTERM)
		cmd.Process.Kill()
	}
}

// shellCommand runs a command line with sh in its own process group, so
// cancelling ctx stops everything it started.
func shellCommand(ctx context.Context, command string) *exec.Cmd {
	cmd := sysproc.CommandContext(ctx, "/bin/sh", "-c", command)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	return cmd
}
