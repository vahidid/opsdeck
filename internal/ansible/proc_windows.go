//go:build windows

package ansible

import "os/exec"

// Ansible does not run natively on Windows (its control node needs a
// POSIX system, e.g. WSL); these keep the package building.
func setProcessGroup(cmd *exec.Cmd) {}

func interrupt(cmd *exec.Cmd) { kill(cmd) }

func kill(cmd *exec.Cmd) {
	if cmd.Process != nil {
		cmd.Process.Kill()
	}
}
