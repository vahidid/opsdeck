package workspace

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// notExported are the top-level folders left out of an export: local-only
// state (agent sessions, known_hosts, run history), Git working copies, and
// what the agent produced (reports, artifacts).
var notExported = map[string]bool{".opsdeck": true, "repos": true, "reports": true, "artifacts": true, ".git": true}

// Files returns the shareable files of the workspace (inventory, docs,
// skills, scripts, Ansible projects…) keyed by slash-separated path.
func (w *Workspace) Files() (map[string][]byte, error) {
	out := map[string][]byte{}
	err := filepath.WalkDir(w.Path, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(w.Path, p)
		if err != nil || rel == "." {
			return err
		}
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if notExported[rel] {
				return filepath.SkipDir
			}
			return nil
		}
		// symlinks and other special files may point outside the folder
		if !d.Type().IsRegular() || strings.HasSuffix(rel, ".tmp") {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		out[rel] = b
		return nil
	})
	return out, err
}

// CreateFromFiles makes a new workspace in the workspaces root from files
// returned by Files (the receiving end of an export).
func (m *Manager) CreateFromFiles(name string, files map[string][]byte) (*Workspace, error) {
	if !nameRe.MatchString(name) || name == "global" {
		return nil, fmt.Errorf("invalid workspace name %q", name)
	}
	if _, ok := files["workspace.yaml"]; !ok {
		return nil, errors.New("workspace.yaml is missing")
	}
	for rel := range files {
		top, _, _ := strings.Cut(rel, "/")
		if !filepath.IsLocal(filepath.FromSlash(rel)) || path.Clean(rel) != rel || strings.Contains(rel, `\`) || notExported[top] {
			return nil, fmt.Errorf("invalid file path %q", rel)
		}
	}
	if _, err := m.Get(name); err == nil {
		return nil, fmt.Errorf("a workspace named %q already exists", name)
	}
	dir := filepath.Join(m.root, name)
	if _, err := os.Stat(dir); err == nil {
		return nil, fmt.Errorf("folder %s already exists", dir)
	}
	w, err := writeFiles(dir, files)
	if err != nil {
		os.RemoveAll(dir)
		return nil, err
	}
	if w.Name() != name {
		os.RemoveAll(dir)
		return nil, fmt.Errorf("workspace.yaml names the workspace %q, not %q", w.Name(), name)
	}
	return w, nil
}

func writeFiles(dir string, files map[string][]byte) (*Workspace, error) {
	if err := os.MkdirAll(filepath.Join(dir, ".opsdeck"), 0o700); err != nil {
		return nil, err
	}
	for rel, b := range files {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			return nil, err
		}
		if err := os.WriteFile(p, b, 0o600); err != nil {
			return nil, err
		}
	}
	return Load(dir)
}
