package update

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"opsdeck/internal/sysproc"
)

// runner does what touches the system, so tests can stand in for it.
type runner interface {
	lookPath(name string) (string, error)
	// owns: the package manager query succeeds (the file belongs to a package)
	owns(name string, args ...string) bool
	installPackage(method, file string) error
	// relaunchAfterExit starts a step that waits for this process to end,
	// installs file when given and starts target again.
	relaunchAfterExit(goos, method, file, target string) error
}

type osRunner struct{}

func (osRunner) lookPath(name string) (string, error) { return exec.LookPath(name) }

func (osRunner) owns(name string, args ...string) bool {
	if _, err := exec.LookPath(name); err != nil {
		return false
	}
	return sysproc.Command(name, args...).Run() == nil
}

// installPackage installs a .deb or .rpm as root, asking for the password
// in the desktop's polkit dialog.
func (r osRunner) installPackage(method, file string) error {
	var args []string
	has := func(n string) bool { _, err := exec.LookPath(n); return err == nil }
	switch {
	case method == "deb" && has("apt-get"):
		args = []string{"apt-get", "install", "-y", "--allow-downgrades", file}
	case method == "deb":
		args = []string{"dpkg", "-i", file}
	case has("dnf"):
		args = []string{"dnf", "install", "-y", file}
	case has("zypper"):
		args = []string{"zypper", "--non-interactive", "install", "--allow-unsigned-rpm", file}
	default:
		args = []string{"rpm", "-U", "--replacepkgs", file}
	}
	out, err := sysproc.Command("pkexec", args...).CombinedOutput()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && (ee.ExitCode() == 126 || ee.ExitCode() == 127) {
			return errors.New("the update was not authorized")
		}
		return fmt.Errorf("%s: %v\n%s", strings.Join(args[:1], " "), err, tail(string(out), 15))
	}
	return nil
}

func tail(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

func (osRunner) relaunchAfterExit(goos, method, file, target string) error {
	pid := strconv.Itoa(os.Getpid())
	var cmd *exec.Cmd
	switch goos {
	case "windows":
		var script string
		if method == "installer" {
			// /S: silent; /D: the folder it is installed in (must come last, unquoted)
			script = `Start-Process -FilePath ` + psq(file) + ` -ArgumentList ` + psq(`/S /D=`+filepath.Dir(target)) + ` -Wait; `
		}
		script = `$ErrorActionPreference = 'SilentlyContinue'; Wait-Process -Id ` + pid + `; ` + script + `Start-Process -FilePath ` + psq(target)
		cmd = sysproc.Command("powershell", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-WindowStyle", "Hidden", "-Command", script)
	case "darwin":
		// copy the new bundle next to the old one, then swap them, so a
		// failure leaves the installed app as it was
		const script = `pid=$1 dmg=$2 app=$3
while kill -0 "$pid" 2>/dev/null; do sleep 0.3; done
mnt=$(mktemp -d /tmp/opsdeck-update.XXXXXX)
if hdiutil attach -nobrowse -readonly -mountpoint "$mnt" "$dmg" >/dev/null; then
  src=$(ls -d "$mnt"/*.app | head -n 1)
  rm -rf "$app.new"
  if [ -n "$src" ] && cp -R "$src" "$app.new"; then
    rm -rf "$app" && mv "$app.new" "$app"
  fi
  hdiutil detach "$mnt" -quiet
fi
xattr -dr com.apple.quarantine "$app" 2>/dev/null
open "$app"`
		cmd = sysproc.Command("/bin/sh", "-c", script, "sh", pid, file, target)
	default:
		cmd = sysproc.Command("/bin/sh", "-c", `while kill -0 "$1" 2>/dev/null; do sleep 0.3; done; exec "$2"`, "sh", pid, target)
	}
	sysproc.Detach(cmd)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = nil, nil, nil
	if err := cmd.Start(); err != nil {
		return err
	}
	go cmd.Wait()
	return nil
}

// psq quotes a PowerShell string.
func psq(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }
