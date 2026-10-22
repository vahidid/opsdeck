// Package config stores user settings (no secrets: API keys are vault references).
package config

import (
	"os"
	"path/filepath"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
)

// Provider is a model provider. Type "anthropic" uses the Messages API;
// "openai" is any OpenAI-compatible Chat Completions API; "claude-code",
// "codex", "gemini-cli" and "acp" run a locally installed agent CLI with the
// user's own login (see internal/extagent).
type Provider struct {
	ID        string   `yaml:"id" json:"id"`
	Type      string   `yaml:"type" json:"type"`
	Name      string   `yaml:"name" json:"name"`
	BaseURL   string   `yaml:"baseUrl" json:"baseUrl"`
	APIKeyRef string   `yaml:"apiKeyRef,omitempty" json:"apiKeyRef"`
	Models    []string `yaml:"models" json:"models"`
	// External agents (claude-code, codex, gemini-cli, acp): the local CLI to run.
	Command string            `yaml:"command,omitempty" json:"command"`
	Args    []string          `yaml:"args,omitempty" json:"args"`
	Env     map[string]string `yaml:"env,omitempty" json:"env"`
}

type Settings struct {
	AutoLockMinutes int         `yaml:"autoLockMinutes" json:"autoLockMinutes"`
	DefaultModel    string      `yaml:"defaultModel,omitempty" json:"defaultModel"`
	Providers       []Provider  `yaml:"providers" json:"providers"`
	LocalAccess     LocalAccess `yaml:"localAccess" json:"localAccess"`
	VPN             VPN         `yaml:"vpn" json:"vpn"`
	Git             Git         `yaml:"git" json:"git"`
}

// Git holds the commit author for working copies ("" = the access token's
// owner on GitLab/GitHub).
type Git struct {
	Name  string `yaml:"name,omitempty" json:"name"`
	Email string `yaml:"email,omitempty" json:"email"`
}

// VPN holds machine-level VPN settings.
type VPN struct {
	// Elevate runs system-mode VPN clients (openvpn, openconnect, wg-quick)
	// with: auto (sudo -n when opsdeck is not root), sudo, pkexec or none.
	Elevate string `yaml:"elevate,omitempty" json:"elevate"`
	// Browser is the Chrome/Edge/Chromium/Brave used for SSO login windows
	// ("" = detect).
	Browser string `yaml:"browser,omitempty" json:"browser"`
}

// LocalAccess controls the agent's tools for this computer (files outside the
// workspace and local commands). Every write and every local command needs
// approval unless the user allowed it; reads outside AllowedDirs ask once
// per folder.
type LocalAccess struct {
	Disabled bool `yaml:"disabled,omitempty" json:"disabled"`
	// AllowedDirs can be read (listed, searched) without asking.
	AllowedDirs []string `yaml:"allowedDirs,omitempty" json:"allowedDirs"`
	// AutoReadCommands runs local commands classified read-only without asking.
	AutoReadCommands bool `yaml:"autoReadCommands,omitempty" json:"autoReadCommands"`
}

type file struct {
	Settings           Settings `yaml:"settings"`
	ExternalWorkspaces []string `yaml:"externalWorkspaces,omitempty"`
	// TrustedMCP holds hashes of MCP server definitions the user approved
	// on this machine; others (e.g. from a cloned workspace) never start.
	TrustedMCP []string `yaml:"trustedMcp,omitempty"`
	// GitConnections are the GitHub/GitLab accounts connected on this
	// machine (connectors); their tokens live in the vault.
	GitConnections []GitConnection `yaml:"gitConnections,omitempty"`
}

// GitConnection is a connected GitHub or GitLab account.
type GitConnection struct {
	ID       string `yaml:"id" json:"id"`
	Provider string `yaml:"provider" json:"provider"` // github | gitlab
	URL      string `yaml:"url" json:"url"`           // web root: https://github.com, https://gitlab.example.com
	APIURL   string `yaml:"apiUrl,omitempty" json:"apiUrl"`
	CACert   string `yaml:"caCert,omitempty" json:"caCert"`
	// Credential is the vault item with the token (vault://global/…).
	Credential string `yaml:"credential" json:"credential"`
	Method     string `yaml:"method" json:"method"` // token | oauth | gh | glab
	// OAuthClientID is the OAuth application used to sign in (refreshes).
	OAuthClientID string    `yaml:"oauthClientId,omitempty" json:"oauthClientId"`
	Login         string    `yaml:"login,omitempty" json:"login"`
	Name          string    `yaml:"name,omitempty" json:"name"`
	Created       time.Time `yaml:"created" json:"created"`
}

// Store is safe for concurrent use.
type Store struct {
	mu   sync.Mutex
	path string
	f    file
}

func Load(path string) (*Store, error) {
	s := &Store{path: path, f: file{Settings: Settings{AutoLockMinutes: 15, Providers: []Provider{}}}}
	b, err := os.ReadFile(path)
	if err == nil {
		if err := yaml.Unmarshal(b, &s.f); err != nil {
			return nil, err
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	if s.f.Settings.Providers == nil {
		s.f.Settings.Providers = []Provider{}
	}
	return s, nil
}

func (s *Store) save() error {
	b, err := yaml.Marshal(s.f)
	if err != nil {
		return err
	}
	os.MkdirAll(filepath.Dir(s.path), 0o700)
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func (s *Store) Settings() Settings {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.f.Settings
	c.Providers = append([]Provider(nil), c.Providers...)
	c.LocalAccess.AllowedDirs = append([]string{}, c.LocalAccess.AllowedDirs...)
	return c
}

func (s *Store) SetSettings(v Settings) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if v.Providers == nil {
		v.Providers = []Provider{}
	}
	s.f.Settings = v
	return s.save()
}

func (s *Store) ExternalWorkspaces() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.f.ExternalWorkspaces...)
}

func (s *Store) SetExternalWorkspaces(paths []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.f.ExternalWorkspaces = paths
	return s.save()
}

func (s *Store) GitConnections() []GitConnection {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]GitConnection{}, s.f.GitConnections...)
}

// PutGitConnection adds c or replaces the connection with its ID.
func (s *Store) PutGitConnection(c GitConnection) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, x := range s.f.GitConnections {
		if x.ID == c.ID {
			s.f.GitConnections[i] = c
			return s.save()
		}
	}
	s.f.GitConnections = append(s.f.GitConnections, c)
	return s.save()
}

func (s *Store) DeleteGitConnection(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.f.GitConnections[:0]
	for _, x := range s.f.GitConnections {
		if x.ID != id {
			out = append(out, x)
		}
	}
	s.f.GitConnections = out
	return s.save()
}

func (s *Store) MCPTrusted(hash string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, h := range s.f.TrustedMCP {
		if h == hash {
			return true
		}
	}
	return false
}

func (s *Store) TrustMCP(hashes ...string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	changed := false
	for _, h := range hashes {
		found := false
		for _, x := range s.f.TrustedMCP {
			if x == h {
				found = true
			}
		}
		if !found {
			s.f.TrustedMCP = append(s.f.TrustedMCP, h)
			changed = true
		}
	}
	if !changed {
		return nil
	}
	return s.save()
}
