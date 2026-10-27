package vpn

import (
	"runtime"
	"strings"
	"sync"

	"opsdeck/internal/sysproc"
)

// Touch ID for sudo on macOS. sudo itself takes Touch ID only through
// pam_tid in /etc/pam.d, which macOS keeps apps out of, so opsdeck keeps the
// user's password in its vault and asks for a fingerprint instead: before a
// command runs through sudo -A, sudoLogin gives sudo that password (sudo -v),
// and sudo, which then remembers it for opsdeck's process, asks for nothing.
// The first time opsdeck asks for the password in a dialog. Without Touch
// ID, or when the user cancels, sudo asks in the dialog of askpass.go.

// SudoPassword loads and saves the user's password for sudo (in the vault;
// "" = none or the vault is locked). Set by the hub; nil = not kept.
var SudoPassword struct {
	Load func() string
	Save func(string)
}

var sudoMu sync.Mutex

func sudoLogin() {
	if runtime.GOOS != "darwin" || SudoPassword.Load == nil {
		return
	}
	sudoMu.Lock()
	defer sudoMu.Unlock()
	if sysproc.Command("sudo", "-n", "true").Run() == nil {
		return // sudo asks for no password
	}
	if pw := SudoPassword.Load(); pw != "" {
		if touchID("start a system-mode VPN") != "ok" {
			return
		}
		if sudoValidate(pw) {
			return
		}
		// the password changed: ask for it again
	} else if touchID("") != "ok" {
		return // no fingerprint: nothing to keep the password behind
	}
	pw, err := askPassword("opsdeck needs your Mac password to start system-mode VPNs. It keeps it in its vault and asks for your fingerprint from now on.")
	if err == nil && sudoValidate(pw) {
		SudoPassword.Save(pw)
	}
}

// sudoValidate gives sudo the password (sudo -v); false = wrong.
func sudoValidate(pw string) bool {
	cmd := sysproc.Command("sudo", "-S", "-v", "-p", "")
	cmd.Stdin = strings.NewReader(pw + "\n")
	return cmd.Run() == nil
}

// touchIDScript asks for a fingerprint (LocalAuthentication) with the reason
// in argv[0]; an empty reason only checks that one is enrolled.
const touchIDScript = `ObjC.import('LocalAuthentication');
function run(argv) {
  var ctx = $.LAContext.alloc.init;
  if (!ctx.canEvaluatePolicyError($.LAPolicyDeviceOwnerAuthenticationWithBiometrics, null)) return 'unavailable';
  if (!argv[0]) return 'ok';
  var res = '';
  ctx.evaluatePolicyLocalizedReasonReply($.LAPolicyDeviceOwnerAuthenticationWithBiometrics, argv[0], function (ok) { res = ok ? 'ok' : 'denied'; });
  while (!res) $.NSRunLoop.currentRunLoop.runUntilDate($.NSDate.dateWithTimeIntervalSinceNow(0.1));
  return res;
}`

// touchID returns "ok", "denied" or "unavailable".
func touchID(reason string) string {
	out, err := sysproc.Command("osascript", "-l", "JavaScript", "-e", touchIDScript, reason).Output()
	if err != nil {
		return "unavailable"
	}
	return strings.TrimSpace(string(out))
}
