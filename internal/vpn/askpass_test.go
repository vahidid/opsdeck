package vpn

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

// The test binary answers as the askpass helper, like opsdeck's executable.
func TestMain(m *testing.M) {
	if AskpassMain() {
		return
	}
	os.Exit(m.Run())
}

func TestElevatedCommand(t *testing.T) {
	t.Setenv("SUDO_ASKPASS", "")
	full, env := elevatedCommand([]string{"openconnect", "--protocol=fortinet"}, []string{"sudo", "-A"})
	if strings.Join(full, " ") != "sudo -A openconnect --protocol=fortinet" {
		t.Fatalf("command: %q", full)
	}
	exe, _ := os.Executable()
	if !slices.Contains(env, "SUDO_ASKPASS="+exe) || !slices.Contains(env, askpassVar+"=openconnect") {
		t.Fatalf("env lacks the askpass: %q", env)
	}

	// the user's own askpass is kept
	t.Setenv("SUDO_ASKPASS", "/usr/bin/ssh-askpass")
	_, env = elevatedCommand([]string{"wg-quick", "up", "x"}, []string{"sudo", "-A"})
	if slices.Contains(env, "SUDO_ASKPASS="+exe) || !slices.Contains(env, "SUDO_ASKPASS=/usr/bin/ssh-askpass") {
		t.Fatalf("env: %q", env)
	}

	for _, e := range [][]string{{"sudo", "-n"}, {"pkexec"}, nil} {
		if _, env := elevatedCommand([]string{"openvpn"}, e); env != nil {
			t.Fatalf("%q: env %q", e, env)
		}
	}
	if got := nonInteractive([]string{"sudo", "-A"}); strings.Join(got, " ") != "sudo -n" {
		t.Fatalf("nonInteractive: %q", got)
	}
}

func TestSudoHint(t *testing.T) {
	for line, want := range map[string]string{
		"sudo: a password is required":                                 "sudo needs your password",
		"sudo: 3 incorrect password attempts":                          "sudo: wrong password",
		"vahid is not in the sudoers file.":                            "may not run the VPN client",
		askpassMsg + "error: the password dialog was cancelled":        "sudo: the password dialog was cancelled",
		askpassMsg + "asking for the sudo password in a system dialog": "",
		"Connected as 10.0.0.2":                                        "",
	} {
		if got := sudoHint(line); (want == "") != (got == "") || !strings.Contains(got, want) {
			t.Errorf("sudoHint(%q) = %q, want %q", line, got, want)
		}
	}
}

// askpass runs the test binary as sudo runs its askpass, with a fake zenity
// that writes its arguments to a file and exits with code.
func askpass(t *testing.T, code string) (stdout, stderr, args string, err error) {
	if runtime.GOOS != "linux" {
		t.Skip("the dialogs tested are Linux's")
	}
	dir := t.TempDir()
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + filepath.Join(dir, "args") + "\nprintf 'pa55 word\\n'\nexit " + code + "\n"
	if err := os.WriteFile(filepath.Join(dir, "zenity"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "[sudo] password for alice: ")
	cmd.Env = append(os.Environ(), "PATH="+dir, "DISPLAY=:0", "XDG_CURRENT_DESKTOP=GNOME", askpassVar+"=openconnect")
	var out, errb strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &errb
	err = cmd.Run()
	b, _ := os.ReadFile(filepath.Join(dir, "args"))
	return out.String(), errb.String(), string(b), err
}

func TestAskpass(t *testing.T) {
	out, _, args, err := askpass(t, "0")
	if err != nil {
		t.Fatal(err)
	}
	if out != "pa55 word\n" {
		t.Fatalf("password printed: %q", out)
	}
	if !strings.Contains(args, "--hide-text") || !strings.Contains(args, "openconnect") || !strings.Contains(args, "[sudo] password for alice:") {
		t.Fatalf("dialog arguments: %q", args)
	}
}

func TestAskpassCancelled(t *testing.T) {
	out, errOut, _, err := askpass(t, "1")
	if err == nil || out != "" {
		t.Fatalf("a cancelled dialog gave %q (%v)", out, err)
	}
	var hint string
	for _, l := range strings.Split(errOut, "\n") {
		if h := sudoHint(l); h != "" {
			hint = h
		}
	}
	if hint != "sudo: the password dialog was cancelled" {
		t.Fatalf("hint %q from %q", hint, errOut)
	}
}
