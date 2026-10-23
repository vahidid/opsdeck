//go:build windows

package agent

import (
	"context"
	"opsdeck/internal/sysproc"
	"os/exec"
)

func shellCommand(ctx context.Context, command string) *exec.Cmd {
	return sysproc.CommandContext(ctx, "cmd", "/C", command)
}
