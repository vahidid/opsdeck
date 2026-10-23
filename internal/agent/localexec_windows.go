//go:build windows

package agent

import (
	"context"
	"os/exec"

	"opsdeck/internal/sysproc"
)

func shellCommand(ctx context.Context, command string) *exec.Cmd {
	return sysproc.CommandContext(ctx, "cmd", "/C", command)
}
