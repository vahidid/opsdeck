// Package sysproc starts child processes and hands URLs and files to the
// desktop. Every command opsdeck runs goes through Command/CommandContext so
// that console programs (git, ssh, ansible, agent CLIs…) started from the
// desktop app on Windows, a GUI process, don't each open a console window.
package sysproc

import (
	"context"
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
