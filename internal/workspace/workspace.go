// Package workspace loads and stores workspace definitions: plain YAML and
// Markdown files that never contain secrets, only vault:// references.
package workspace

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"
)

type Meta struct {
	Name        string   `yaml:"name" json:"name"`
	Title       string   `yaml:"title,omitempty" json:"title"`
	Description string   `yaml:"description,omitempty" json:"description"`
	Color       string   `yaml:"color,omitempty" json:"color"`
	Tags        []string `yaml:"tags,omitempty" json:"tags"`
}

type Environment struct {
	Name      string `yaml:"name" json:"name"`
	Protected bool   `yaml:"protected,omitempty" json:"protected"`
}

type Settings struct {
	DefaultUser  string `yaml:"defaultUser,omitempty" json:"defaultUser"`
	DefaultAuth  string `yaml:"defaultAuth,omitempty" json:"defaultAuth"`
	DefaultModel string `yaml:"defaultModel,omitempty" json:"defaultModel"`
}

// Rule is a permission rule for agent tool calls. First matching rule wins.
type Rule struct {
	Effect      string   `yaml:"effect" json:"effect"` // allow | ask | deny
	Risk        string   `yaml:"risk,omitempty" json:"risk,omitempty"`
	Tool        string   `yaml:"tool,omitempty" json:"tool,omitempty"`
	Environment []string `yaml:"environment,omitempty" json:"environment,omitempty"`
	Match       string   `yaml:"match,omitempty" json:"match,omitempty"` // glob on the command / target
}

type Permissions struct {
	Rules []Rule `yaml:"rules,omitempty" json:"rules"`
}

type MCPServer struct {
	Name    string            `yaml:"name" json:"name"`
	Command string            `yaml:"command" json:"command"`
	Args    []string          `yaml:"args,omitempty" json:"args"`
	Env     map[string]string `yaml:"env,omitempty" json:"env"`
}

// Workspace is the content of workspace.yaml plus its loaded resources.
type Workspace struct {
	APIVersion   string        `yaml:"apiVersion" json:"-"`
	Kind         string        `yaml:"kind" json:"-"`
	Metadata     Meta          `yaml:"metadata" json:"-"`
	Environments []Environment `yaml:"environments,omitempty" json:"environments"`
	Settings     Settings      `yaml:"settings,omitempty" json:"settings"`
	Permissions  Permissions   `yaml:"permissions,omitempty" json:"permissions"`
	MCPServers   []MCPServer   `yaml:"mcpServers,omitempty" json:"mcpServers"`

	Path      string      `yaml:"-" json:"path"`
	Resources []*Resource `yaml:"-" json:"-"`
}

func (w *Workspace) Name() string { return w.Metadata.Name }

// Resource is one entry of the inventory.
type Resource struct {
	Kind        string         `yaml:"kind" json:"kind"`
	Name        string         `yaml:"name" json:"name"`
	Environment string         `yaml:"environment,omitempty" json:"environment,omitempty"`
	Tags        []string       `yaml:"tags,omitempty" json:"tags,omitempty"`
	Via         string         `yaml:"via,omitempty" json:"via,omitempty"`
	Description string         `yaml:"description,omitempty" json:"description,omitempty"`
	Docs        string         `yaml:"docs,omitempty" json:"docs,omitempty"`
	Spec        map[string]any `yaml:"spec,omitempty" json:"spec"`

	source string // file it was loaded from, relative to the workspace
}

// ID is "<kind>/<name>".
func (r *Resource) ID() string { return r.Kind + "/" + r.Name }

// Str returns a string spec field.
func (r *Resource) Str(key string) string {
	switch v := r.Spec[key].(type) {
	case string:
		return v
	case int:
		return strconv.Itoa(v)
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	}
	return ""
}

// Int returns a numeric spec field or def.
func (r *Resource) Int(key string, def int) int {
	switch v := r.Spec[key].(type) {
	case int:
		return v
	case float64:
		return int(v)
	case string:
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

// List returns a list spec field.
func (r *Resource) List(key string) []string {
	switch v := r.Spec[key].(type) {
	case []any:
		out := []string{}
		for _, x := range v {
			if s, ok := x.(string); ok && s != "" {
				out = append(out, s)
			}
		}
		return out
	case []string:
		return v
	case string:
		if v == "" {
			return nil
		}
		parts := strings.Split(v, ",")
		for i := range parts {
			parts[i] = strings.TrimSpace(parts[i])
		}
		return parts
	}
	return nil
}

func (r *Resource) HasTag(t string) bool  { return contains(r.Tags, t) }
func (r *Resource) HasRole(t string) bool { return contains(r.List("roles"), t) }

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// Find returns the resource with kind/name.
func (w *Workspace) Find(kind, name string) *Resource {
	for _, r := range w.Resources {
		if r.Kind == kind && r.Name == name {
			return r
		}
	}
	return nil
}

// FindRef resolves "host/x", "network/x", "Host/x" style references.
func (w *Workspace) FindRef(ref string) *Resource {
	kind, name, ok := strings.Cut(ref, "/")
	if !ok {
		return nil
	}
	switch strings.ToLower(kind) {
	case "network", "net":
		for _, r := range w.Resources {
			if r.Name == name && isNetworkKind(r.Kind) {
				return r
			}
		}
		return nil
	}
	for _, r := range w.Resources {
		if strings.EqualFold(r.Kind, kind) && r.Name == name {
			return r
		}
	}
	// Short aliases: panel/x, cluster/x, group/x, db/x
	alias := map[string]string{"cluster": "KubernetesCluster", "k8s": "KubernetesCluster", "group": "HostGroup", "db": "Database", "git": "GitRemote"}
	if k, ok := alias[strings.ToLower(kind)]; ok {
		return w.Find(k, name)
	}
	return nil
}

// FindTarget accepts "app-1" (a host name) or a full reference.
func (w *Workspace) FindTarget(target string) *Resource {
	if strings.Contains(target, "/") {
		return w.FindRef(target)
	}
	if r := w.Find("Host", target); r != nil {
		return r
	}
	if r := w.Find("HostGroup", target); r != nil {
		return r
	}
	for _, r := range w.Resources {
		if r.Name == target {
			return r
		}
	}
	return nil
}

// GroupMembers expands a HostGroup into hosts.
func (w *Workspace) GroupMembers(g *Resource) []*Resource {
	names := g.List("hosts")
	roles, tags := g.List("roles"), g.List("tags")
	var out []*Resource
	for _, r := range w.Resources {
		if r.Kind != "Host" {
			continue
		}
		match := contains(names, r.Name)
		for _, role := range roles {
			match = match || r.HasRole(role)
		}
		for _, t := range tags {
			match = match || r.HasTag(t)
		}
		if match {
			out = append(out, r)
		}
	}
	return out
}

// EnvironmentOf returns the environment definition of a resource.
func (w *Workspace) EnvironmentOf(r *Resource) Environment {
	for _, e := range w.Environments {
		if r != nil && e.Name == r.Environment {
			return e
		}
	}
	if r != nil {
		return Environment{Name: r.Environment}
	}
	return Environment{}
}

// ----------------------------------------------------------------------------
// Manager

var nameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

// Manager finds workspaces under the default root plus registered external paths.
type Manager struct {
	mu       sync.Mutex
	root     string
	external []string
	// onExternalChange persists the list of external paths.
	onExternalChange func([]string) error
}

func NewManager(root string, external []string, persist func([]string) error) *Manager {
	return &Manager{root: root, external: append([]string(nil), external...), onExternalChange: persist}
}

func (m *Manager) dirs() []string {
	var out []string
	entries, _ := os.ReadDir(m.root)
	for _, e := range entries {
		if e.IsDir() {
			out = append(out, filepath.Join(m.root, e.Name()))
		}
	}
	return append(out, m.external...)
}

// List loads every workspace (errors are skipped).
func (m *Manager) List() []*Workspace {
	m.mu.Lock()
	dirs := m.dirs()
	m.mu.Unlock()
	var out []*Workspace
	seen := map[string]bool{}
	for _, d := range dirs {
		w, err := Load(d)
		if err != nil || seen[w.Name()] {
			continue
		}
		seen[w.Name()] = true
		out = append(out, w)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name() < out[j].Name() })
	return out
}

// Get loads a workspace fresh from disk (so external edits are picked up).
func (m *Manager) Get(name string) (*Workspace, error) {
	for _, w := range m.List() {
		if w.Name() == name {
			return w, nil
		}
	}
	return nil, fmt.Errorf("workspace %q not found", name)
}

// Create makes a new workspace folder with starter files.
func (m *Manager) Create(meta Meta) (*Workspace, error) {
	if !nameRe.MatchString(meta.Name) {
		return nil, errors.New("name must be lowercase letters, digits and dashes (e.g. acme-prod)")
	}
	if meta.Name == "global" {
		return nil, errors.New(`"global" is reserved`)
	}
	if _, err := m.Get(meta.Name); err == nil {
		return nil, fmt.Errorf("workspace %q already exists", meta.Name)
	}
	if meta.Title == "" {
		meta.Title = meta.Name
	}
	dir := filepath.Join(m.root, meta.Name)
	if err := os.MkdirAll(filepath.Join(dir, "docs"), 0o700); err != nil {
		return nil, err
	}
	os.MkdirAll(filepath.Join(dir, "resources"), 0o700)
	os.MkdirAll(filepath.Join(dir, "skills"), 0o700)
	w := &Workspace{
		APIVersion: "opsdeck/v1", Kind: "Workspace", Metadata: meta, Path: dir,
		Environments: []Environment{{Name: "dev"}, {Name: "staging"}, {Name: "prod", Protected: true}},
	}
	if err := w.Save(); err != nil {
		return nil, err
	}
	writeIfMissing(filepath.Join(dir, "OPSDECK.md"), fmt.Sprintf(opsdeckTemplate, meta.Title))
	writeIfMissing(filepath.Join(dir, "docs", "README.md"), "# "+meta.Title+"\n\nProject documentation: architecture, contacts, runbooks.\n")
	writeIfMissing(filepath.Join(dir, ".gitignore"), ".opsdeck/\n")
	return Load(dir)
}

const opsdeckTemplate = `# %s — instructions for the agent

<!-- This file is given to the AI agent as context for every session in this workspace.
     Describe how this project works and the rules to follow. -->

## Overview
- What this project is, main services, where it runs.

## Access
- How the network is reached (VPN, bastion), conventions for users and keys.

## Rules
- Never restart production databases without approval.
- Prefer ` + "`docker compose`" + ` in /opt/<app> for deployments.
`

func writeIfMissing(path, content string) {
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		os.WriteFile(path, []byte(content), 0o600)
	}
}

// Import registers an existing folder containing workspace.yaml.
func (m *Manager) Import(path string) (*Workspace, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	w, err := Load(abs)
	if err != nil {
		return nil, err
	}
	if _, err := m.Get(w.Name()); err == nil {
		return nil, fmt.Errorf("a workspace named %q already exists", w.Name())
	}
	m.mu.Lock()
	m.external = append(m.external, abs)
	ext := append([]string(nil), m.external...)
	m.mu.Unlock()
	if m.onExternalChange != nil {
		if err := m.onExternalChange(ext); err != nil {
			return nil, err
		}
	}
	return w, nil
}

// Delete removes a workspace folder (managed ones) or unregisters it (external ones).
func (m *Manager) Delete(name string) error {
	w, err := m.Get(name)
	if err != nil {
		return err
	}
	m.mu.Lock()
	for i, p := range m.external {
		if p == w.Path {
			m.external = append(m.external[:i], m.external[i+1:]...)
			ext := append([]string(nil), m.external...)
			m.mu.Unlock()
			if m.onExternalChange != nil {
				return m.onExternalChange(ext)
			}
			return nil
		}
	}
	m.mu.Unlock()
	if filepath.Dir(w.Path) != filepath.Clean(m.root) {
		return errors.New("refusing to delete a folder outside the workspaces root")
	}
	return os.RemoveAll(w.Path)
}

// ----------------------------------------------------------------------------
// Loading and saving

// Load reads workspace.yaml and all resource files of a directory.
func Load(dir string) (*Workspace, error) {
	b, err := os.ReadFile(filepath.Join(dir, "workspace.yaml"))
	if err != nil {
		return nil, err
	}
	var w Workspace
	if err := yaml.Unmarshal(b, &w); err != nil {
		return nil, fmt.Errorf("%s/workspace.yaml: %w", dir, err)
	}
	if w.Metadata.Name == "" {
		w.Metadata.Name = filepath.Base(dir)
	}
	// The name selects the vault scope and the state directory: it must be a
	// plain slug and never "global".
	if !nameRe.MatchString(w.Metadata.Name) || w.Metadata.Name == "global" {
		return nil, fmt.Errorf("%s/workspace.yaml: invalid workspace name %q", dir, w.Metadata.Name)
	}
	if w.Metadata.Tags == nil {
		w.Metadata.Tags = []string{}
	}
	w.Path = dir
	files := []string{}
	if _, err := os.Stat(filepath.Join(dir, "resources.yaml")); err == nil {
		files = append(files, "resources.yaml")
	}
	for _, pattern := range []string{"resources/*.yaml", "resources/*.yml"} {
		matches, _ := filepath.Glob(filepath.Join(dir, pattern))
		sort.Strings(matches)
		for _, m := range matches {
			rel, _ := filepath.Rel(dir, m)
			files = append(files, rel)
		}
	}
	for _, f := range files {
		b, err := os.ReadFile(filepath.Join(dir, f))
		if err != nil {
			return nil, err
		}
		var list []*Resource
		if err := yaml.Unmarshal(b, &list); err != nil {
			return nil, fmt.Errorf("%s: %w", f, err)
		}
		for _, r := range list {
			if r == nil || r.Kind == "" || r.Name == "" {
				continue
			}
			if r.Spec == nil {
				r.Spec = map[string]any{}
			}
			r.source = f
			w.Resources = append(w.Resources, r)
		}
	}
	return &w, nil
}

// Save writes workspace.yaml.
func (w *Workspace) Save() error {
	w.APIVersion, w.Kind = "opsdeck/v1", "Workspace"
	b, err := yaml.Marshal(w)
	if err != nil {
		return err
	}
	return atomicWrite(filepath.Join(w.Path, "workspace.yaml"), b)
}

// PutResource adds or replaces (by oldID, "" for new) a resource and saves.
func (w *Workspace) PutResource(oldID string, r *Resource) error {
	if _, ok := KindByName(r.Kind); !ok {
		return fmt.Errorf("unknown kind %q", r.Kind)
	}
	if !nameRe.MatchString(r.Name) {
		return errors.New("resource name must be lowercase letters, digits and dashes")
	}
	if r.Spec == nil {
		r.Spec = map[string]any{}
	}
	idx := -1
	for i, x := range w.Resources {
		if x.ID() == oldID {
			idx = i
		}
		if x.ID() == r.ID() && x.ID() != oldID {
			return fmt.Errorf("%s already exists", r.ID())
		}
	}
	touched := map[string]bool{}
	if idx >= 0 {
		r.source = w.Resources[idx].source
		w.Resources[idx] = r
	} else {
		if oldID != "" {
			return fmt.Errorf("%s not found", oldID)
		}
		r.source = defaultFile(r.Kind)
		w.Resources = append(w.Resources, r)
	}
	touched[r.source] = true
	return w.saveFiles(touched)
}

// DeleteResource removes a resource and saves.
func (w *Workspace) DeleteResource(id string) error {
	for i, r := range w.Resources {
		if r.ID() == id {
			w.Resources = append(w.Resources[:i], w.Resources[i+1:]...)
			return w.saveFiles(map[string]bool{r.source: true})
		}
	}
	return fmt.Errorf("%s not found", id)
}

func defaultFile(kind string) string {
	switch kind {
	case "WireGuard", "SocksProxy":
		return "resources/networks.yaml"
	case "Host", "HostGroup":
		return "resources/hosts.yaml"
	case "KubernetesCluster":
		return "resources/clusters.yaml"
	default:
		return "resources/services.yaml"
	}
}

func (w *Workspace) saveFiles(files map[string]bool) error {
	for f := range files {
		var list []*Resource
		for _, r := range w.Resources {
			if r.source == f {
				list = append(list, r)
			}
		}
		path := filepath.Join(w.Path, f)
		if len(list) == 0 {
			if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
			continue
		}
		b, err := yaml.Marshal(list)
		if err != nil {
			return err
		}
		os.MkdirAll(filepath.Dir(path), 0o700)
		if err := atomicWrite(path, b); err != nil {
			return err
		}
	}
	return nil
}

func atomicWrite(path string, b []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
