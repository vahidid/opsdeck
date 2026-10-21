package agent

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// LocalPolicy is the agent's access to the computer opsdeck runs on: files
// outside the workspace and local commands. Reads inside the workspace and
// AllowedDirs run directly; other reads ask once per folder (per file for
// credential locations); writes and commands always ask unless the user
// allowed them for the session. opsdeck's own vault, state and config are
// never reachable through the file tools.
type LocalPolicy struct {
	Home             string
	AllowedDirs      []string // readable without asking (the workspace folder is added per session)
	Forbidden        []string // files and folders the file tools refuse
	AutoReadCommands bool     // run local commands classified read-only without asking
}

// Credential locations: readable only with consent for each file, and files
// holding private keys are never shown to the model (import them instead).
var sensitiveHomeDirs = []string{".ssh", ".gnupg", ".aws", ".azure", ".kube", ".docker", ".config/gcloud",
	".password-store", ".terraform.d", ".vault-token", ".netrc", ".git-credentials", ".pgpass", ".npmrc", ".pypirc",
	".config/gh", ".config/op", ".local/share/keyrings"}

func (p *LocalPolicy) caseInsensitive() bool {
	return runtime.GOOS == "darwin" || runtime.GOOS == "windows"
}

func (p *LocalPolicy) under(path, dir string) bool {
	if dir == "" {
		return false
	}
	if p.caseInsensitive() {
		path, dir = strings.ToLower(path), strings.ToLower(dir)
	}
	if path == dir {
		return true
	}
	return strings.HasPrefix(path, strings.TrimSuffix(dir, string(filepath.Separator))+string(filepath.Separator))
}

// Resolve expands ~, requires an absolute path and cleans it. It refuses
// opsdeck's own files (including every workspace's .opsdeck state folder),
// checking both the path and where its symlinks lead. The path is returned
// as given, so consent and grants follow what the user sees.
func (p *LocalPolicy) Resolve(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", errors.New("path is empty")
	}
	if path == "~" || strings.HasPrefix(path, "~/") || strings.HasPrefix(path, `~\`) {
		path = filepath.Join(p.Home, path[1:])
	}
	if !filepath.IsAbs(path) {
		return "", fmt.Errorf("%q is not absolute: use an absolute path or ~/... (home is %s)", path, p.Home)
	}
	path = filepath.Clean(path)
	for _, c := range []string{path, realPath(path)} {
		if p.forbidden(c) {
			return "", fmt.Errorf("%s belongs to opsdeck itself (vault, state or settings) and is not accessible to the agent", path)
		}
	}
	return path, nil
}

// realPath resolves symlinks of the file, or of its folder when it does not
// exist yet.
func realPath(path string) string {
	if r, err := filepath.EvalSymlinks(path); err == nil {
		return r
	}
	if r, err := filepath.EvalSymlinks(filepath.Dir(path)); err == nil {
		return filepath.Join(r, filepath.Base(path))
	}
	return path
}

func (p *LocalPolicy) forbidden(path string) bool {
	for _, f := range p.Forbidden {
		if p.under(path, f) {
			return true
		}
	}
	// <workspace>/.opsdeck holds sessions, pinned host keys and agent state.
	for dir := path; ; {
		if filepath.Base(dir) == ".opsdeck" {
			if _, err := os.Stat(filepath.Join(filepath.Dir(dir), "workspace.yaml")); err == nil {
				return true
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return false
		}
		dir = parent
	}
}

// Sensitive reports whether a path, or where it links to, is a credential location.
func (p *LocalPolicy) Sensitive(path string) bool {
	return p.sensitive(path) || p.sensitive(realPath(path))
}

func (p *LocalPolicy) sensitive(path string) bool {
	for _, d := range sensitiveHomeDirs {
		if p.under(path, filepath.Join(p.Home, filepath.FromSlash(d))) {
			return true
		}
	}
	base := strings.ToLower(filepath.Base(path))
	return strings.HasPrefix(base, "id_") || strings.HasSuffix(base, ".pem") || strings.HasSuffix(base, ".key") ||
		strings.HasSuffix(base, ".p12") || strings.HasSuffix(base, ".pfx") || strings.HasSuffix(base, ".kdbx") ||
		base == ".env" || strings.HasPrefix(base, ".env.")
}

// Allowed reports whether a path may be read without asking.
func (p *LocalPolicy) Allowed(path string, extra ...string) bool {
	for _, d := range append(append([]string(nil), p.AllowedDirs...), extra...) {
		if p.under(path, d) {
			return true
		}
	}
	return false
}

// readAccess sets the consent and grant of a local read on path (a file or,
// for listings and searches, a folder).
func (p *LocalPolicy) readAccess(c *Call, env *Env, path string, isDir bool) {
	c.Environment = "local"
	if p.Sensitive(path) {
		c.Consent = "reads a credential location on this computer"
		c.Grant, c.GrantLabel = "local-read-file:"+path, "Always allow reading "+path+" in this session"
		return
	}
	extra := []string{}
	if env.WS != nil {
		extra = append(extra, env.WS.Path)
	}
	if p.Allowed(path, extra...) {
		return
	}
	dir := path
	if !isDir {
		dir = filepath.Dir(path)
	}
	c.Consent = "reads outside the workspace and the allowed folders"
	c.Grant, c.GrantLabel = "local-read:"+dir, "Always allow reading in "+dir+" in this session"
	// a grant on a parent folder covers this one
	c.parentGrants = parentGrants("local-read:", dir)
}

// writeAccess marks a local write: always approved by the user (or by an
// "always allow" for that folder), in every mode.
func (p *LocalPolicy) writeAccess(c *Call, path string) {
	c.Environment, c.Protected = "local", true
	if p.Sensitive(path) {
		c.Grant, c.GrantLabel = "local-write-file:"+path, "Always allow writing "+path+" in this session"
		return
	}
	dir := filepath.Dir(path)
	c.Grant, c.GrantLabel = "local-write:"+dir, "Always allow writing files in "+dir+" in this session"
	c.parentGrants = parentGrants("local-write:", dir)
}

func parentGrants(prefix, dir string) []string {
	var out []string
	for d := filepath.Dir(dir); ; d = filepath.Dir(d) {
		out = append(out, prefix+d)
		if filepath.Dir(d) == d {
			return out
		}
	}
}

func isBinary(b []byte) bool {
	if len(b) > 8000 {
		b = b[:8000]
	}
	return bytes.IndexByte(b, 0) >= 0
}

// holdsPrivateKey reports whether content contains a private key; such
// files are never shown to the model.
func holdsPrivateKey(b []byte) bool {
	s := string(b)
	return strings.Contains(s, "PRIVATE KEY-----") || strings.Contains(s, "PuTTY-User-Key-File")
}
