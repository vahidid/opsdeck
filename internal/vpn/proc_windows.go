//go:build windows

package vpn

import "os/exec"

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
