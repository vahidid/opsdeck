//go:build !windows

package sysproc

import (
	"os/exec"
	"syscall"
)

func noWindow(cmd *exec.Cmd) {}

func detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}
