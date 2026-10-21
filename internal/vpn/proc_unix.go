//go:build !windows

package vpn

import (
	"os/exec"
	"syscall"
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
