package vpn

import (
	"context"
	"errors"
	"os"
	"regexp"
	"runtime"
	"strings"

	"opsdeck/internal/sysproc"
)

// Touch ID for sudo on macOS: with pam_tid in /etc/pam.d/sudo_local (read by
// sudo since macOS 14, and kept by system updates), sudo asks for a
// fingerprint before it asks for the password, so system-mode VPNs start
// with Touch ID; the password dialog (askpass.go) remains the fallback.

const (
	pamSudo      = "/etc/pam.d/sudo"
	pamSudoLocal = "/etc/pam.d/sudo_local"
	pamTID       = "/usr/lib/pam/pam_tid.so.2"
)

var pamTIDLine = regexp.MustCompile(`(?m)^[ \t]*auth[ \t]+\S+[ \t]+pam_tid\.so`)

// TouchID tells whether sudo can take Touch ID on this computer.
type TouchID struct {
	Available bool `json:"available"` // a Mac whose sudo reads sudo_local
	Enabled   bool `json:"enabled"`   // sudo_local turns pam_tid on
}

// TouchIDStatus reports whether sudo can take Touch ID here.
func TouchIDStatus() TouchID {
	if runtime.GOOS != "darwin" {
		return TouchID{}
	}
	sudo, err := os.ReadFile(pamSudo)
	if err != nil || !strings.Contains(string(sudo), "sudo_local") {
		return TouchID{}
	}
	if _, err := os.Stat(pamTID); err != nil {
		return TouchID{}
	}
	local, _ := os.ReadFile(pamSudoLocal)
	return TouchID{Available: true, Enabled: pamTIDLine.Match(local)}
}

// enableTouchIDScript adds pam_tid to sudo_local, after what is there (a
// pam_reattach line must come first). It runs as root.
const enableTouchIDScript = `set -e
f=` + pamSudoLocal + `
if [ -f "$f" ] && grep -Eq '^[[:space:]]*auth[[:space:]]+[^[:space:]]+[[:space:]]+pam_tid\.so' "$f"; then exit 0; fi
if [ ! -f "$f" ]; then
  printf '# sudo_local: local config file which survives system update and is included for sudo\n' > "$f"
fi
printf '# Touch ID for sudo (turned on by opsdeck)\nauth       sufficient     pam_tid.so\n' >> "$f"
chown root:wheel "$f"
chmod 444 "$f"
`

// EnableTouchID turns on Touch ID for sudo. macOS asks for an administrator
// in its own dialog to change sudo_local.
func EnableTouchID(ctx context.Context) error {
	st := TouchIDStatus()
	if !st.Available {
		return errors.New("Touch ID for sudo needs macOS 14 or later")
	}
	if st.Enabled {
		return nil
	}
	out, err := sysproc.CommandContext(ctx, "osascript",
		"-e", "on run argv",
		"-e", "do shell script (item 1 of argv) with prompt (item 2 of argv) with administrator privileges",
		"-e", "end run",
		enableTouchIDScript, "opsdeck wants to turn on Touch ID for sudo, to start system-mode VPNs with your fingerprint.",
	).CombinedOutput()
	if err != nil {
		if strings.Contains(string(out), "-128") {
			return errors.New("cancelled")
		}
		return errors.New(strings.TrimSpace(string(out)))
	}
	if !TouchIDStatus().Enabled {
		return errors.New("sudo_local does not turn on pam_tid")
	}
	return nil
}
