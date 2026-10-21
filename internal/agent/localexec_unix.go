//go:build !windows

package agent

import (
	"context"
	"os/exec"
	"syscall"
)

// shellCommand runs command with sh in its own process group, so a timeout or
// interrupt stops everything it started.
func shellCommand(ctx context.Context, command string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", command)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	return cmd
}
