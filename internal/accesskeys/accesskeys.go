// Package accesskeys keeps the keys other agents (Claude Code, Codex, Cursor,
// Gemini CLI…) use to reach a workspace through opsdeck's MCP server. Only a
// hash of each key is stored; the key itself is shown once, when it is made.
package accesskeys

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Prefix starts every key, so a leaked one is easy to recognise and scan for.
const Prefix = "odk_"

// Modes an access key can have; they are the agent modes of its session:
// plan only reads, ask asks before every change, auto runs what the
// workspace's rules allow and asks for the rest.
var Modes = []string{"plan", "ask", "auto"}

type Key struct {
	ID        string    `json:"id"`
	Label     string    `json:"label"`
	Workspace string    `json:"workspace"`
	Mode      string    `json:"mode"`
	Hint      string    `json:"hint"` // the first characters, to recognise it
	Created   time.Time `json:"created"`
	LastUsed  time.Time `json:"lastUsed,omitzero"`
	Hash      string    `json:"hash,omitempty"`
}

// Public is the key without its hash.
func (k Key) Public() Key {
	k.Hash = ""
	return k
}

type Store struct {
	path string
	mu   sync.Mutex
	keys []Key
	// saved is when LastUsed was last written: use is recorded at most once
	// a minute per key, not on every call.
	saved map[string]time.Time
}

func Open(path string) (*Store, error) {
	s := &Store{path: path, saved: map[string]time.Time{}}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, &s.keys); err != nil {
		return nil, err
	}
	return s, nil
}

func validMode(m string) bool {
	for _, x := range Modes {
		if x == m {
			return true
		}
	}
	return false
}

// Create makes a key and returns it with its secret, which is not kept.
func (s *Store) Create(label, workspace, mode string) (Key, string, error) {
	label = strings.TrimSpace(label)
	if label == "" || workspace == "" {
		return Key{}, "", errors.New("a label and a workspace are required")
	}
	if !validMode(mode) {
		return Key{}, "", errors.New("mode must be plan, ask or auto")
	}
	raw := make([]byte, 24)
	rand.Read(raw)
	secret := Prefix + hex.EncodeToString(raw)
	id := make([]byte, 5)
	rand.Read(id)
	k := Key{ID: hex.EncodeToString(id), Label: label, Workspace: workspace, Mode: mode, Hint: secret[:10], Created: time.Now().UTC(), Hash: hash(secret)}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.keys = append(s.keys, k)
	if err := s.saveLocked(); err != nil {
		s.keys = s.keys[:len(s.keys)-1]
		return Key{}, "", err
	}
	return k.Public(), secret, nil
}

// List returns the keys (without hashes), newest first.
func (s *Store) List() []Key {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Key, 0, len(s.keys))
	for _, k := range s.keys {
		out = append(out, k.Public())
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Created.After(out[j].Created) })
	return out
}

// Update changes a key's label or mode.
func (s *Store) Update(id, label, mode string) (Key, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.keys {
		if s.keys[i].ID != id {
			continue
		}
		if label = strings.TrimSpace(label); label != "" {
			s.keys[i].Label = label
		}
		if mode != "" {
			if !validMode(mode) {
				return Key{}, errors.New("mode must be plan, ask or auto")
			}
			s.keys[i].Mode = mode
		}
		return s.keys[i].Public(), s.saveLocked()
	}
	return Key{}, os.ErrNotExist
}

// Revoke deletes a key; agents using it are refused from then on.
func (s *Store) Revoke(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.keys {
		if s.keys[i].ID == id {
			s.keys = append(s.keys[:i], s.keys[i+1:]...)
			return s.saveLocked()
		}
	}
	return os.ErrNotExist
}

// RevokeWorkspace deletes the keys of a workspace that is removed.
func (s *Store) RevokeWorkspace(ws string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	kept := s.keys[:0]
	for _, k := range s.keys {
		if k.Workspace != ws {
			kept = append(kept, k)
		}
	}
	s.keys = kept
	s.saveLocked()
}

// Lookup finds the key of a secret and records that it was used.
func (s *Store) Lookup(secret string) (Key, bool) {
	if !strings.HasPrefix(secret, Prefix) {
		return Key{}, false
	}
	h := []byte(hash(secret))
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.keys {
		if subtle.ConstantTimeCompare([]byte(s.keys[i].Hash), h) == 1 {
			now := time.Now().UTC()
			s.keys[i].LastUsed = now
			if now.Sub(s.saved[s.keys[i].ID]) > time.Minute {
				s.saved[s.keys[i].ID] = now
				s.saveLocked()
			}
			return s.keys[i].Public(), true
		}
	}
	return Key{}, false
}

func (s *Store) saveLocked() error {
	b, err := json.MarshalIndent(s.keys, "", "  ")
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

func hash(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}
