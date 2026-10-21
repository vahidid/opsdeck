//go:build darwin

package vpn

import "syscall"

func sysProcAttr(elevated bool) *syscall.SysProcAttr { return &syscall.SysProcAttr{} }

func hasNetAdmin() bool { return false }
