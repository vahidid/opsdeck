// Package paths resolves where opsdeck keeps its data, state and config.
package paths

import (
	"os"
	"path/filepath"
	"runtime"
)

// Dirs holds the base directories used by opsdeck.
type Dirs struct {
	Data   string // vault, workspaces
	State  string // sessions, audit, known_hosts, caches
	Config string // config.yaml
}

// Default returns OS-conventional directories. OPSDECK_HOME overrides all of them
// with subdirectories of a single root (useful for tests and portable installs).
func Default() Dirs {
	if root := os.Getenv("OPSDECK_HOME"); root != "" {
		return Dirs{
			Data:   filepath.Join(root, "data"),
			State:  filepath.Join(root, "state"),
			Config: filepath.Join(root, "config"),
		}
	}
	home, _ := os.UserHomeDir()
	switch runtime.GOOS {
	case "darwin":
		base := filepath.Join(home, "Library", "Application Support", "opsdeck")
		return Dirs{Data: base, State: filepath.Join(base, "state"), Config: base}
	case "windows":
		appData := os.Getenv("APPDATA")
		if appData == "" {
			appData = filepath.Join(home, "AppData", "Roaming")
		}
		base := filepath.Join(appData, "opsdeck")
		return Dirs{Data: base, State: filepath.Join(base, "state"), Config: base}
	default:
		return Dirs{
			Data:   filepath.Join(xdg("XDG_DATA_HOME", filepath.Join(home, ".local", "share")), "opsdeck"),
			State:  filepath.Join(xdg("XDG_STATE_HOME", filepath.Join(home, ".local", "state")), "opsdeck"),
			Config: filepath.Join(xdg("XDG_CONFIG_HOME", filepath.Join(home, ".config")), "opsdeck"),
		}
	}
}

func xdg(env, fallback string) string {
	if v := os.Getenv(env); v != "" {
		return v
	}
	return fallback
}

// Ensure creates all directories with private permissions.
func (d Dirs) Ensure() error {
	for _, p := range []string{d.Data, d.State, d.Config, d.Workspaces()} {
		if err := os.MkdirAll(p, 0o700); err != nil {
			return err
		}
	}
	return nil
}

func (d Dirs) VaultFile() string  { return filepath.Join(d.Data, "vault.db") }
func (d Dirs) Workspaces() string { return filepath.Join(d.Data, "workspaces") }
func (d Dirs) ConfigFile() string { return filepath.Join(d.Config, "config.yaml") }
func (d Dirs) AuditDir() string   { return filepath.Join(d.State, "audit") }
func (d Dirs) WorkspaceState(ws string) string {
	return filepath.Join(d.State, "workspaces", ws)
}
