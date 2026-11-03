// Package sysproc starts child processes and hands URLs and files to the
// desktop. Every command opsdeck runs goes through Command/CommandContext so
// that console programs (git, ssh, ansible, agent CLIs…) started from the
// desktop app on Windows, a GUI process, don't each open a console window.
package sysproc

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

// Command is exec.Command for opsdeck's child processes.
func Command(name string, args ...string) *exec.Cmd {
	cmd := exec.Command(name, args...)
	noWindow(cmd)
	return cmd
}

// CommandContext is exec.CommandContext for opsdeck's child processes.
func CommandContext(ctx context.Context, name string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, args...)
	noWindow(cmd)
	return cmd
}

// Detach makes cmd outlive opsdeck: its own session (Unix) or a process
// without a console (Windows). Used for steps that run after opsdeck quits.
func Detach(cmd *exec.Cmd) { detach(cmd) }

// Open opens a URL in the default browser (or a file or folder in its
// default application).
func Open(target string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = Command("open", target)
	case "windows":
		cmd = Command("rundll32", "url.dll,FileProtocolHandler", target)
	default:
		cmd = Command("xdg-open", target)
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	go cmd.Wait()
	return nil
}

// Reveal shows a file in the system file manager (its folder on Linux,
// where file managers have no common "select" option).
func Reveal(path string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = Command("open", "-R", path)
	case "windows":
		cmd = Command("explorer", "/select,", path)
	default:
		cmd = Command("xdg-open", filepath.Dir(path))
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	go cmd.Wait()
	return nil
}

// Notify shows a desktop notification: notify-send on Linux (libnotify),
// Notification Center on macOS, a toast on Windows. Title and body are
// passed as arguments or environment, never as code.
func Notify(title, body string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = Command("osascript", "-e", "on run argv", "-e", "display notification (item 2 of argv) with title (item 1 of argv) sound name \"Glass\"", "-e", "end run", title, body)
	case "windows":
		ps := `[Windows.UI.Notifications.ToastNotificationManager, Windows.UI.Notifications, ContentType = WindowsRuntime] > $null
$t = [Windows.UI.Notifications.ToastNotificationManager]::GetTemplateContent([Windows.UI.Notifications.ToastTemplateType]::ToastText02)
$x = $t.GetElementsByTagName('text')
$x.Item(0).AppendChild($t.CreateTextNode($env:OPSDECK_NOTIFY_TITLE)) > $null
$x.Item(1).AppendChild($t.CreateTextNode($env:OPSDECK_NOTIFY_BODY)) > $null
[Windows.UI.Notifications.ToastNotificationManager]::CreateToastNotifier('{1AC14E77-02E7-4E5D-B744-2EB1AE5198B7}\WindowsPowerShell\v1.0\powershell.exe').Show([Windows.UI.Notifications.ToastNotification]::new($t))`
		cmd = Command("powershell", "-NoProfile", "-NonInteractive", "-Command", ps)
		cmd.Env = append(os.Environ(), "OPSDECK_NOTIFY_TITLE="+title, "OPSDECK_NOTIFY_BODY="+body)
	default:
		if _, err := exec.LookPath("notify-send"); err != nil {
			return err
		}
		cmd = Command("notify-send", "--app-name=opsdeck", "--urgency=normal", "--icon=dialog-warning", title, body)
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	go cmd.Wait()
	return nil
}
