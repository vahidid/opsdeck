package paths

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// Downloads is the user's downloads folder.
func Downloads() string {
	if d := downloadsKnownFolder(); d != "" {
		return d
	}
	home, _ := os.UserHomeDir()
	if runtime.GOOS == "linux" {
		// xdg-user-dir knows a localized or moved folder (~/Téléchargements…)
		if out, err := exec.Command("xdg-user-dir", "DOWNLOAD").Output(); err == nil {
			if d := strings.TrimSpace(string(out)); d != "" && filepath.Clean(d) != filepath.Clean(home) {
				return d
			}
		}
	}
	return filepath.Join(home, "Downloads")
}
