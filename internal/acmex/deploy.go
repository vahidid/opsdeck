package acmex

import (
	"context"
	"errors"
	"fmt"
	"path"
	"strings"

	"opsdeck/internal/connect"
	"opsdeck/internal/workspace"
)

// DeployOptions say where a certificate goes on a host. Empty paths are
// not written.
type DeployOptions struct {
	// CertPath receives the full chain (leaf first) — what nginx, Caddy and
	// most servers want as the certificate file.
	CertPath string `json:"certPath"`
	// KeyPath receives the private key (mode 0600).
	KeyPath string `json:"keyPath"`
	// LeafPath receives the leaf only and ChainPath the intermediates
	// (Apache's SSLCertificateFile / SSLCertificateChainFile).
	LeafPath  string `json:"leafPath,omitempty"`
	ChainPath string `json:"chainPath,omitempty"`
	// BundlePath receives chain and key in one file (HAProxy), mode 0600.
	BundlePath string `json:"bundlePath,omitempty"`
}

// Files lists the files a deployment writes, in order.
func (o DeployOptions) Files() []string {
	var out []string
	for _, p := range []string{o.CertPath, o.KeyPath, o.LeafPath, o.ChainPath, o.BundlePath} {
		if strings.TrimSpace(p) != "" {
			out = append(out, strings.TrimSpace(p))
		}
	}
	return out
}

// Validate checks the paths (absolute, no duplicates, something to write).
func (o DeployOptions) Validate() error {
	files := o.Files()
	if len(files) == 0 {
		return errors.New("nothing to write: give at least a certificate or key path")
	}
	seen := map[string]bool{}
	for _, f := range files {
		if !path.IsAbs(f) {
			return fmt.Errorf("%s: paths must be absolute", f)
		}
		if seen[f] {
			return fmt.Errorf("%s is named twice", f)
		}
		seen[f] = true
	}
	return nil
}

// DefaultPaths are the usual places for a certificate's files.
func DefaultPaths(domain string) DeployOptions {
	d := strings.TrimPrefix(domain, "*.")
	return DeployOptions{CertPath: "/etc/ssl/" + d + "/fullchain.pem", KeyPath: "/etc/ssl/" + d + "/privkey.pem"}
}

// Deploy writes a stored certificate to a host over SFTP (the key with mode
// 0600). It returns the files written.
func (m *Manager) Deploy(ctx context.Context, hub *connect.Hub, ws *workspace.Workspace, certName string, host *workspace.Resource, o DeployOptions) ([]string, error) {
	if err := o.Validate(); err != nil {
		return nil, err
	}
	chain, key, _, err := m.Material(ws, certName)
	if err != nil {
		return nil, err
	}
	leaf, rest := SplitChain(chain)
	// create the folders (as the SSH user; a root-owned folder then needs
	// the user to prepare it or sudo)
	dirs := map[string]bool{}
	for _, f := range o.Files() {
		dirs[path.Dir(f)] = true
	}
	for d := range dirs {
		hub.Exec(ctx, ws, host, "mkdir -p -- '"+strings.ReplaceAll(d, "'", `'\''`)+"'", nil, 1024)
	}
	type file struct {
		path, content string
		mode          uint32
	}
	var files []file
	add := func(p, content string, mode uint32) {
		if strings.TrimSpace(p) != "" {
			files = append(files, file{strings.TrimSpace(p), content, mode})
		}
	}
	add(o.CertPath, chain, 0o644)
	add(o.KeyPath, key, 0o600)
	add(o.LeafPath, leaf, 0o644)
	add(o.ChainPath, rest, 0o644)
	add(o.BundlePath, strings.TrimRight(chain, "\n")+"\n"+key, 0o600)
	var written []string
	for _, f := range files {
		if err := hub.WriteFileMode(ctx, ws, host, f.path, f.content, fsMode(f.mode)); err != nil {
			return written, fmt.Errorf("%s: %v (if permission denied, prepare the folder for the SSH user or let the agent write it with sudo through exec)", f.path, err)
		}
		written = append(written, f.path)
	}
	return written, nil
}
