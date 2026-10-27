package vpn

import (
	"context"
	"errors"
	"fmt"
	"html"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// sudo has no terminal to ask for the user's password when opsdeck starts a
// system-mode VPN client, so opsdeck runs it as `sudo -A` with itself as
// SUDO_ASKPASS: sudo then starts opsdeck's executable with its prompt, and
// AskpassMain shows the system's password dialog (osascript on macOS,
// zenity, kdialog or an ssh-askpass on Linux desktops) and prints the
// password to sudo. With passwordless sudo, or while sudo remembers the
// password, no dialog opens.

// askpassVar marks opsdeck's executable as sudo's askpass helper; its value
// names what needs root ("openconnect").
const askpassVar = "OPSDECK_SUDO_ASKPASS"

// askpassMsg starts the lines the helper writes to sudo's output (the tunnel
// log); sudoHint turns its errors into the connection's error.
const askpassMsg = "opsdeck askpass: "

// elevatedCommand returns argv run through elevate, and the environment
// sudo -A needs to ask for the password with opsdeck (nil = inherit).
func elevatedCommand(argv, elevate []string) (full, env []string) {
	full = append(append([]string{}, elevate...), argv...)
	if len(elevate) < 2 || elevate[0] != "sudo" || elevate[1] != "-A" {
		return full, nil
	}
	sudoLogin()
	env = os.Environ()
	if os.Getenv("SUDO_ASKPASS") == "" { // the user's own askpass wins
		exe, err := os.Executable()
		if err != nil {
			full[1] = "-n"
			return full, nil
		}
		env = append(env, "SUDO_ASKPASS="+exe)
	}
	return full, append(env, askpassVar+"="+argv[0])
}

// nonInteractive is elevate for commands that must not ask for a password
// (polled statistics): sudo -n instead of -A.
func nonInteractive(elevate []string) []string {
	if len(elevate) >= 2 && elevate[0] == "sudo" && elevate[1] == "-A" {
		return append([]string{"sudo", "-n"}, elevate[2:]...)
	}
	return elevate
}

// sudoHint explains a line of sudo (or of the askpass helper) that means it
// could not get root ("" = not such a line).
func sudoHint(line string) string {
	switch {
	case strings.HasPrefix(line, askpassMsg+"error: "):
		return "sudo: " + strings.TrimPrefix(line, askpassMsg+"error: ")
	case strings.Contains(line, "incorrect password attempt"):
		return "sudo: wrong password"
	case strings.Contains(line, "is not in the sudoers file") || strings.Contains(line, "is not allowed to execute"):
		return "your user may not run the VPN client with sudo: " + strings.TrimSpace(line)
	case strings.HasPrefix(line, "sudo: a password is required") || strings.HasPrefix(line, "sudo: no password was provided") ||
		strings.HasPrefix(line, "sudo: a terminal is required"):
		return "sudo needs your password and got none: enter it in the password dialog, allow passwordless sudo for the VPN client, or choose pkexec in App settings"
	}
	return ""
}

// AskpassMain runs opsdeck as sudo's askpass helper when sudo started it for
// a VPN client: it asks for the password in a dialog, prints it and exits.
// It returns false when opsdeck was started for something else.
func AskpassMain() bool {
	what := os.Getenv(askpassVar)
	if what == "" {
		return false
	}
	prompt := "Password:"
	if len(os.Args) > 1 && strings.TrimSpace(os.Args[1]) != "" {
		prompt = strings.TrimSpace(os.Args[1])
	}
	fmt.Fprintln(os.Stderr, askpassMsg+"asking for the sudo password in a system dialog")
	pw, err := askPassword(fmt.Sprintf("opsdeck needs administrator rights to start %s for a system-mode VPN.\n\n%s", what, prompt))
	if err != nil {
		fmt.Fprintln(os.Stderr, askpassMsg+"error: "+err.Error())
		os.Exit(1)
	}
	fmt.Println(pw)
	os.Exit(0)
	return true
}

// passwordDialog is a program that asks for a password and prints it.
type passwordDialog struct {
	bin  string
	args func(text string) []string
}

func askPassword(text string) (string, error) {
	dialogs, err := passwordDialogs()
	if err != nil {
		return "", err
	}
	// sudo gives up (the connection timed out or was cancelled): close the dialog
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func(ppid int) {
		for os.Getppid() == ppid {
			select {
			case <-ctx.Done():
				return
			case <-time.After(500 * time.Millisecond):
			}
		}
		cancel()
	}(os.Getppid())
	for _, d := range dialogs {
		path, err := exec.LookPath(d.bin)
		if err != nil {
			continue
		}
		out, err := exec.CommandContext(ctx, path, d.args(text)...).Output()
		if err != nil {
			var ee *exec.ExitError
			if errors.As(err, &ee) {
				return "", errors.New("the password dialog was cancelled")
			}
			return "", err
		}
		return strings.TrimRight(string(out), "\r\n"), nil
	}
	return "", errors.New("no password dialog found: install zenity, kdialog or ssh-askpass, allow passwordless sudo for the VPN client, or choose pkexec in App settings")
}

// passwordDialogs lists the programs that can ask here, preferred first.
func passwordDialogs() ([]passwordDialog, error) {
	switch runtime.GOOS {
	case "darwin":
		return []passwordDialog{{"osascript", func(text string) []string {
			return []string{
				"-e", "on run argv",
				"-e", `display dialog (item 1 of argv) default answer "" with hidden answer with title "opsdeck" with icon caution buttons {"Cancel", "OK"} default button "OK"`,
				"-e", "return text returned of result",
				"-e", "end run",
				text,
			}
		}}}, nil
	case "windows":
		return nil, errors.New("sudo cannot ask for a password on Windows")
	}
	if os.Getenv("DISPLAY") == "" && os.Getenv("WAYLAND_DISPLAY") == "" {
		return nil, errors.New("no desktop session to show a password dialog in: allow passwordless sudo for the VPN client, or run opsdeck as root")
	}
	zenity := func(text string) []string { // markup: escape the prompt
		return []string{"--entry", "--hide-text", "--title=opsdeck", "--text=" + html.EscapeString(text)}
	}
	kdialog := func(text string) []string { return []string{"--title", "opsdeck", "--password", text} }
	askpass := func(text string) []string { return []string{text} }
	ds := []passwordDialog{
		{"zenity", zenity},
		{"kdialog", kdialog},
		{"yad", zenity},
		{"qarma", zenity},
		{"ksshaskpass", askpass},
		{"lxqt-openssh-askpass", askpass},
		{"ssh-askpass", askpass},
		{"/usr/lib/ssh/ssh-askpass", askpass},
		{"/usr/libexec/openssh/ssh-askpass", askpass},
		{"/usr/libexec/openssh/gnome-ssh-askpass", askpass},
		{"/usr/lib/openssh/gnome-ssh-askpass", askpass},
	}
	if strings.Contains(strings.ToUpper(os.Getenv("XDG_CURRENT_DESKTOP")), "KDE") {
		ds[0], ds[1] = ds[1], ds[0]
	}
	return ds, nil
}
