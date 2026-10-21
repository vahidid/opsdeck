//go:build linux

package vpn

import (
	"os"
	"strconv"
	"strings"
	"syscall"
)

// sysProcAttr stops the client when opsdeck dies (sudo relays the signal).
func sysProcAttr(elevated bool) *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Pdeathsig: syscall.SIGTERM}
}

// hasNetAdmin reports CAP_NET_ADMIN in the effective set.
func hasNetAdmin() bool {
	b, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return false
	}
	for _, l := range strings.Split(string(b), "\n") {
		if v, ok := strings.CutPrefix(l, "CapEff:"); ok {
			n, err := strconv.ParseUint(strings.TrimSpace(v), 16, 64)
			return err == nil && n&(1<<12) != 0
		}
	}
	return false
}
