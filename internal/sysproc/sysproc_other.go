//go:build !windows

package sysproc

import "os/exec"

func noWindow(cmd *exec.Cmd) {}
