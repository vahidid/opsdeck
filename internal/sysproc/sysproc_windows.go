//go:build windows

package sysproc

import (
	"os/exec"
	"syscall"
)

// createNoWindow keeps a console program from opening a console window;
// GUI programs (a browser for single sign-on) ignore it.
const createNoWindow = 0x08000000

func noWindow(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags |= createNoWindow
}
