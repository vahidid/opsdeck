// Package scripts keeps reusable shell scripts (server bootstrap, hardening,
// agent installs…) that opsdeck runs on hosts as one unit: the exact content
// the user approved, with typed parameters and secrets taken from the vault.
//
// Scripts live in two places: the user's own library (in opsdeck's data
// folder, available in every workspace) and a workspace's scripts/ folder
// (shared with the team through git). A workspace script with the same name
// replaces the user's one.
//
// On disk a script is a folder:
//
//	scripts/server-bootstrap/
//	├── script.yaml      # name, description, version, params, check…
//	├── script.sh        # the script itself (any interpreter via its #! line)
//	└── versions/        # earlier versions: v1.yaml + v1-script.sh, v2.yaml + v2-script.sh…
package scripts

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"gopkg.in/yaml.v3"

	"opsdeck/internal/workspace"
)

// Scopes.
const (
	ScopeUser      = "user"
	ScopeWorkspace = "workspace"
)

// Statuses. A script is approved while the hash this user approved matches
// its content; any change (by the agent, a teammate's commit, a hand edit)
// makes it a draft again until the user reviews it. Approvals are kept on
// this computer, never in the shared script files: anyone who can change a
// script could also write its (deterministic) hash there.
const (
	StatusDraft    = "draft"
	StatusApproved = "approved"
)

// MaxSize is the largest script accepted.
const MaxSize = 256 << 10

// ErrNotFound is returned for an unknown script.
var ErrNotFound = errors.New("script not found")

// Param is a typed input of a script, passed to it as an environment variable
// of the same name.
type Param struct {
	Name        string `yaml:"name" json:"name"`
	Description string `yaml:"description,omitempty" json:"description,omitempty"`
	Type        string `yaml:"type,omitempty" json:"type,omitempty"` // string (default) | int | bool | secret
	Default     string `yaml:"default,omitempty" json:"default,omitempty"`
	Required    bool   `yaml:"required,omitempty" json:"required,omitempty"`
}

// Meta is script.yaml.
type Meta struct {
	Name        string   `yaml:"name" json:"name"`
	Description string   `yaml:"description" json:"description"`
	Version     int      `yaml:"version" json:"version"`
	File        string   `yaml:"file,omitempty" json:"file,omitempty"` // default script.sh
	OS          []string `yaml:"os,omitempty" json:"os,omitempty"`     // e.g. ubuntu-22.04, debian, rhel-9
	When        []string `yaml:"when,omitempty" json:"when,omitempty"` // hints for the agent, e.g. new-host
	Tags        []string `yaml:"tags,omitempty" json:"tags,omitempty"`
	Sudo        bool     `yaml:"sudo,omitempty" json:"sudo,omitempty"`
	Risk        string   `yaml:"risk,omitempty" json:"risk,omitempty"` // change (default) | destructive
	TimeoutSec  int      `yaml:"timeoutSec,omitempty" json:"timeoutSec,omitempty"`
	Check       string   `yaml:"check,omitempty" json:"check,omitempty"` // exits 0 when already applied
	Params      []Param  `yaml:"params,omitempty" json:"params,omitempty"`
	Approved    string   `yaml:"-" json:"approved,omitempty"` // hash this user approved (kept locally)
	CreatedBy   string   `yaml:"createdBy,omitempty" json:"createdBy,omitempty"`
	UpdatedBy   string   `yaml:"updatedBy,omitempty" json:"updatedBy,omitempty"`
	Updated     string   `yaml:"updated,omitempty" json:"updated,omitempty"`
}

// Script is a script with its computed state.
type Script struct {
	Meta
	Scope     string `json:"scope"`
	Hash      string `json:"hash"`
	Status    string `json:"status"`
	Overrides bool   `json:"overrides,omitempty"` // a workspace script replacing a user one
	Body      string `json:"body,omitempty"`
	Versions  []int  `json:"versions,omitempty"`
}

// Ref is how the script is named in approvals and logs: name@vN.
func (s *Script) Ref() string { return s.Name + "@v" + strconv.Itoa(s.Version) }

var (
	nameRe   = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)
	paramRe  = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,63}$`)
	osRe     = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,39}$`)
	fileRe   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
	interpRe = regexp.MustCompile(`^[A-Za-z0-9_./+=-]+$`)
)

// ValidName reports whether name can be a script name.
func ValidName(name string) bool { return nameRe.MatchString(name) }

// WorkspaceStore is a workspace's scripts/ folder; approvals live in its
// local-only .opsdeck/ folder.
func WorkspaceStore(w *workspace.Workspace) Store {
	return Store{Dir: filepath.Join(w.Path, "scripts"), Scope: ScopeWorkspace,
		Approvals: filepath.Join(w.StateDir(), "script-approvals.json")}
}

// Store is one folder of scripts.
type Store struct {
	Dir   string
	Scope string
	// Approvals is the local file of approved hashes by script name
	// (default <Dir>/.approvals.json, for a folder that is not shared).
	Approvals string
}

var approvalsMu sync.Mutex

func (s Store) approvalsPath() string {
	if s.Approvals != "" {
		return s.Approvals
	}
	return filepath.Join(s.Dir, ".approvals.json")
}

func (s Store) approvals() map[string]string {
	approvalsMu.Lock()
	defer approvalsMu.Unlock()
	return s.readApprovals()
}

func (s Store) readApprovals() map[string]string {
	m := map[string]string{}
	if b, err := os.ReadFile(s.approvalsPath()); err == nil {
		json.Unmarshal(b, &m)
	}
	return m
}

// setApproval records (or with hash "" removes) the approved hash of a script.
func (s Store) setApproval(name, hash string) error {
	approvalsMu.Lock()
	defer approvalsMu.Unlock()
	m := s.readApprovals()
	if m[name] == hash {
		return nil
	}
	if hash == "" {
		delete(m, name)
	} else {
		m[name] = hash
	}
	b, _ := json.MarshalIndent(m, "", "  ")
	if err := os.MkdirAll(filepath.Dir(s.approvalsPath()), 0o700); err != nil {
		return err
	}
	return writeAtomic(s.approvalsPath(), b, 0o600)
}

func (s Store) dir(name string) (string, error) {
	if !ValidName(name) {
		return "", fmt.Errorf("invalid script name %q (lowercase letters, digits, . _ -)", name)
	}
	return filepath.Join(s.Dir, name), nil
}

// List returns the scripts without their bodies, sorted by name.
func (s Store) List() []Script {
	out := []Script{}
	matches, _ := filepath.Glob(filepath.Join(s.Dir, "*", "script.yaml"))
	approved := s.approvals()
	for _, m := range matches {
		sc, err := s.get(filepath.Base(filepath.Dir(m)), approved)
		if err != nil {
			continue
		}
		sc.Body = ""
		out = append(out, sc)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Get returns a script with its body.
func (s Store) Get(name string) (Script, error) { return s.get(name, s.approvals()) }

func (s Store) get(name string, approved map[string]string) (Script, error) {
	dir, err := s.dir(name)
	if err != nil {
		return Script{}, err
	}
	b, err := os.ReadFile(filepath.Join(dir, "script.yaml"))
	if errors.Is(err, fs.ErrNotExist) {
		return Script{}, fmt.Errorf("%w: %s", ErrNotFound, name)
	}
	if err != nil {
		return Script{}, err
	}
	var m Meta
	if err := yaml.Unmarshal(b, &m); err != nil {
		return Script{}, fmt.Errorf("%s/script.yaml: %v", name, err)
	}
	m.Name = name // the folder name wins
	m.Approved = approved[name]
	if m.File == "" {
		m.File = "script.sh"
	}
	if !fileRe.MatchString(m.File) || m.File == "script.yaml" {
		return Script{}, fmt.Errorf("%s: invalid file %q", name, m.File)
	}
	body, err := os.ReadFile(filepath.Join(dir, m.File))
	if err != nil {
		return Script{}, fmt.Errorf("%s: %v", name, err)
	}
	sc := Script{Meta: m, Scope: s.Scope, Body: string(body)}
	sc.Hash = Hash(&sc)
	sc.Status = StatusDraft
	if m.Approved != "" && m.Approved == sc.Hash {
		sc.Status = StatusApproved
	}
	vs, _ := filepath.Glob(filepath.Join(dir, "versions", "v*.yaml"))
	for _, v := range vs {
		if n, err := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(filepath.Base(v), "v"), ".yaml")); err == nil {
			sc.Versions = append(sc.Versions, n)
		}
	}
	sort.Ints(sc.Versions)
	return sc, nil
}

// Version returns an earlier version of a script.
func (s Store) Version(name string, v int) (Script, error) {
	dir, err := s.dir(name)
	if err != nil {
		return Script{}, err
	}
	sub := Store{Dir: filepath.Join(dir, "versions"), Scope: s.Scope}
	b, err := os.ReadFile(filepath.Join(sub.Dir, fmt.Sprintf("v%d.yaml", v)))
	if err != nil {
		return Script{}, fmt.Errorf("%w: %s version %d", ErrNotFound, name, v)
	}
	var m Meta
	if err := yaml.Unmarshal(b, &m); err != nil {
		return Script{}, err
	}
	if m.File == "" {
		m.File = "script.sh"
	}
	if !fileRe.MatchString(m.File) {
		return Script{}, fmt.Errorf("%s version %d: invalid file %q", name, v, m.File)
	}
	body, err := os.ReadFile(filepath.Join(sub.Dir, fmt.Sprintf("v%d-%s", v, m.File)))
	if err != nil {
		return Script{}, err
	}
	sc := Script{Meta: m, Scope: s.Scope, Body: string(body)}
	sc.Hash = Hash(&sc)
	sc.Status = StatusDraft
	return sc, nil
}

// Hash covers everything that decides what runs on a host: the body, the
// check, sudo, the interpreter file, supported systems and the parameters
// (with their defaults). Descriptions and hints are not part of it.
func Hash(s *Script) string {
	h := sha256.New()
	params, _ := json.Marshal(s.Params)
	fmt.Fprintf(h, "file=%s\nsudo=%t\nos=%s\ncheck=%s\nparams=%s\nbody=\n", s.File, s.Sudo, strings.Join(s.OS, ","), s.Check, params)
	h.Write([]byte(s.Body))
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// Validate checks a script before it is saved or run.
func Validate(s *Script) error {
	if !ValidName(s.Name) {
		return fmt.Errorf("invalid script name %q (lowercase letters, digits, . _ -; at most 64)", s.Name)
	}
	if strings.TrimSpace(s.Description) == "" {
		return errors.New("description is required: say what the script does and when to use it")
	}
	if strings.TrimSpace(s.Body) == "" {
		return errors.New("the script is empty")
	}
	if len(s.Body) > MaxSize {
		return fmt.Errorf("script too large (%d bytes, max %d)", len(s.Body), MaxSize)
	}
	if !utf8.ValidString(s.Body) || strings.ContainsRune(s.Body, 0) {
		return errors.New("the script must be text")
	}
	if s.File == "" {
		s.File = "script.sh"
	}
	if !fileRe.MatchString(s.File) || s.File == "script.yaml" {
		return fmt.Errorf("invalid file name %q", s.File)
	}
	if _, err := Interpreter(s.Body); err != nil {
		return err
	}
	switch s.Risk {
	case "", "change", "destructive":
	default:
		return fmt.Errorf("risk must be change or destructive, not %q", s.Risk)
	}
	if s.TimeoutSec < 0 || s.TimeoutSec > 7200 {
		return errors.New("timeoutSec must be between 0 and 7200")
	}
	for _, o := range s.OS {
		if !osRe.MatchString(o) {
			return fmt.Errorf("invalid os %q (e.g. ubuntu, ubuntu-24.04, debian-12, rhel-9, alpine)", o)
		}
	}
	seen := map[string]bool{}
	for i := range s.Params {
		p := &s.Params[i]
		if !paramRe.MatchString(p.Name) {
			return fmt.Errorf("invalid parameter name %q (letters, digits, _; it becomes an environment variable)", p.Name)
		}
		if strings.HasPrefix(strings.ToUpper(p.Name), "OPSDECK_") {
			return fmt.Errorf("parameter %s: names starting with OPSDECK_ are reserved", p.Name)
		}
		if seen[p.Name] {
			return fmt.Errorf("duplicate parameter %s", p.Name)
		}
		seen[p.Name] = true
		switch p.Type {
		case "", "string":
			p.Type = ""
		case "int", "bool":
			if p.Default != "" {
				if _, err := normalize(p, p.Default); err != nil {
					return err
				}
			}
		case "secret":
			if p.Default != "" && !strings.HasPrefix(p.Default, "vault://") {
				return fmt.Errorf("parameter %s: a secret's default must be a vault:// reference, never the value", p.Name)
			}
		default:
			return fmt.Errorf("parameter %s: type must be string, int, bool or secret", p.Name)
		}
	}
	return nil
}

// Interpreter returns the command that runs the script, from its #! line
// (default bash). The script file is passed as the last argument, so it
// also works where /tmp is mounted noexec.
func Interpreter(body string) ([]string, error) {
	if !strings.HasPrefix(body, "#!") {
		return []string{"bash"}, nil
	}
	line, _, _ := strings.Cut(body[2:], "\n")
	f := strings.Fields(strings.TrimSpace(line))
	if len(f) == 0 {
		return nil, errors.New("empty #! line")
	}
	for _, t := range f {
		if !interpRe.MatchString(t) {
			return nil, fmt.Errorf("unsupported #! line %q", strings.TrimSpace(line))
		}
	}
	return f, nil
}

// Save creates or updates a script. by is "user" or "agent": the user's own
// saves are approved as they are; the agent's are drafts whenever what runs
// changes. A change of what runs bumps the version and keeps the previous
// one under versions/.
func (s Store) Save(in Script, by string) (Script, error) {
	in.Scope = s.Scope
	if err := Validate(&in); err != nil {
		return Script{}, err
	}
	dir, err := s.dir(in.Name)
	if err != nil {
		return Script{}, err
	}
	old, err := s.Get(in.Name)
	exists := err == nil
	if err != nil && !errors.Is(err, ErrNotFound) {
		return Script{}, err
	}
	m := in.Meta
	m.Approved, m.Version, m.CreatedBy = "", 1, by
	m.UpdatedBy, m.Updated = by, time.Now().UTC().Format(time.RFC3339)
	hash := Hash(&in)
	if exists {
		m.CreatedBy = old.CreatedBy
		m.Version = old.Version
		if m.Version < 1 {
			m.Version = 1
		}
		if old.Hash != hash {
			if err := archive(dir, &old); err != nil {
				return Script{}, err
			}
			m.Version = old.Version + 1
		}
		if old.Hash == hash && old.Status == StatusApproved {
			m.Approved = hash
		}
	}
	if by == "user" {
		m.Approved = hash
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return Script{}, err
	}
	if exists && old.File != m.File {
		os.Remove(filepath.Join(dir, old.File))
	}
	if err := writeAtomic(filepath.Join(dir, m.File), []byte(in.Body), 0o755); err != nil {
		return Script{}, err
	}
	if err := writeMeta(filepath.Join(dir, "script.yaml"), &m); err != nil {
		return Script{}, err
	}
	if err := s.setApproval(in.Name, m.Approved); err != nil {
		return Script{}, err
	}
	return s.Get(in.Name)
}

// archive keeps the current version under versions/ before it is replaced.
func archive(dir string, old *Script) error {
	vdir := filepath.Join(dir, "versions")
	if err := os.MkdirAll(vdir, 0o755); err != nil {
		return err
	}
	if err := writeAtomic(filepath.Join(vdir, fmt.Sprintf("v%d-%s", old.Version, old.File)), []byte(old.Body), 0o644); err != nil {
		return err
	}
	m := old.Meta
	return writeMeta(filepath.Join(vdir, fmt.Sprintf("v%d.yaml", old.Version)), &m)
}

// Approve marks the script's current content as reviewed. hash must be the
// one the user saw, so a change made in between is not approved by accident.
func (s Store) Approve(name, hash string) (Script, error) {
	sc, err := s.Get(name)
	if err != nil {
		return Script{}, err
	}
	if hash != sc.Hash {
		return Script{}, fmt.Errorf("the script changed since you opened it (now %s); review it again", sc.Hash)
	}
	if err := s.setApproval(name, sc.Hash); err != nil {
		return Script{}, err
	}
	return s.Get(name)
}

// Delete removes a script with its earlier versions.
func (s Store) Delete(name string) error {
	dir, err := s.dir(name)
	if err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(dir, "script.yaml")); err != nil {
		return fmt.Errorf("%w: %s", ErrNotFound, name)
	}
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	return s.setApproval(name, "")
}

// Copy copies a script (with its versions, and this user's approval) into
// another store, e.g. to pin one of the user's scripts into a workspace for
// the team. Teammates approve the copy themselves.
func Copy(from Store, name string, to Store) error {
	src, err := from.dir(name)
	if err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(src, "script.yaml")); err != nil {
		return fmt.Errorf("%w: %s", ErrNotFound, name)
	}
	dst, _ := to.dir(name)
	if _, err := os.Stat(dst); err == nil {
		return fmt.Errorf("%s already has a script %s", to.Scope, name)
	}
	err = filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if !d.Type().IsRegular() {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		info, _ := d.Info()
		return os.WriteFile(target, b, info.Mode().Perm())
	})
	if err != nil {
		os.RemoveAll(dst)
		return err
	}
	if sc, err := from.Get(name); err == nil && sc.Status == StatusApproved {
		return to.setApproval(name, sc.Hash)
	}
	return nil
}

// Merge combines the user's scripts and a workspace's: a workspace script
// replaces the user's script of the same name.
func Merge(user, ws []Script) []Script {
	own := map[string]bool{}
	out := make([]Script, 0, len(user)+len(ws))
	for _, s := range ws {
		own[s.Name] = true
	}
	for _, s := range user {
		if !own[s.Name] {
			out = append(out, s)
		}
	}
	for _, s := range ws {
		for _, u := range user {
			if u.Name == s.Name {
				s.Overrides = true
			}
		}
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func writeMeta(path string, m *Meta) error {
	c := *m
	if c.File == "script.sh" {
		c.File = ""
	}
	b, err := yaml.Marshal(&c)
	if err != nil {
		return err
	}
	return writeAtomic(path, b, 0o644)
}

func writeAtomic(path string, b []byte, mode os.FileMode) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, mode); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
