// Package config stores user settings (no secrets: API keys are vault references).
package config

import (
	"os"
	"path/filepath"
	"sync"

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
