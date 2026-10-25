package workspace

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// The workspace folder format. workspace.yaml carries it as apiVersion and it
// covers every file of the folder (the resource files have no version of
// their own). See docs/compatibility.md for what a format change may do.
const (
	APIVersion    = "opsdeck/v1"
	FormatVersion = 1
)

// currentFormat is FormatVersion; tests raise it to exercise a migration.
var currentFormat = FormatVersion

// ErrNewerFormat means the folder was written by a newer opsdeck.
var ErrNewerFormat = errors.New("written by a newer version of opsdeck")

// Migration upgrades a workspace from format N to N+1. It gets the parsed
// workspace.yaml and every resource file (path relative to the workspace →
// document) and edits them in place; nothing is written to disk until the
// workspace is next saved.
type Migration func(workspace *yaml.Node, resources map[string]*yaml.Node) error

// migrations[N] upgrades format N to N+1. Format 1 is the first, so there is
// none yet; a format change adds its step here together with a test.
var migrations = map[int]Migration{}

// parseAPIVersion returns the format number of an apiVersion value. A
// workspace.yaml written by hand may leave it out: that is format 1.
func parseAPIVersion(s string) (int, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 1, nil
	}
	n, err := strconv.Atoi(strings.TrimPrefix(s, "opsdeck/v"))
	if !strings.HasPrefix(s, "opsdeck/v") || err != nil || n < 1 {
		return 0, fmt.Errorf("unknown apiVersion %q (expected %s)", s, APIVersion)
	}
	return n, nil
}

// migrate brings the raw documents of a folder from format `from` to the
// current one.
func migrate(from int, ws *yaml.Node, resources map[string]*yaml.Node) error {
	for v := from; v < currentFormat; v++ {
		step := migrations[v]
		if step == nil {
			return fmt.Errorf("no migration from format %d to %d", v, v+1)
		}
		if err := step(ws, resources); err != nil {
			return fmt.Errorf("migrating format %d to %d: %w", v, v+1, err)
		}
	}
	return nil
}
