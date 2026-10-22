//go:build !windows

package ansible

import (
	"os/exec"
	"syscall"
)

// setProcessGroup runs ansible in its own process group, so cancelling a
// run also stops its workers and ssh processes.
func setProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// interrupt asks ansible to stop (it prints what it did so far).
func interrupt(cmd *exec.Cmd) {
	if cmd.Process != nil {
		syscall.Kill(-cmd.Process.Pid, syscall.SIGINT)
	}
}

// kill stops the whole process group.
func kill(cmd *exec.Cmd) {
	if cmd.Process != nil {
		syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}
