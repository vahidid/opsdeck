// Package ansible manages a workspace's Ansible projects (AnsibleProject
// resources): a folder in one of its Git working copies, in the workspace
// itself or anywhere on this computer. It lists and classifies the files
// (playbooks, inventories, variables, roles), reads and writes them, opens
// and writes Ansible Vault files natively, and runs ansible-playbook and
// friends with live logs. Vault passwords are entered by the user and kept
// in memory, or saved in opsdeck's vault; they reach ansible only through
// files in a private temporary folder that lives as long as the run.
package ansible

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"opsdeck/internal/workspace"
)

// Kind is the resource kind of an Ansible project.
const Kind = "AnsibleProject"

// WorkspaceInventory is the inventory generated from the workspace's hosts.
const WorkspaceInventory = "workspace"

const (
	maxFiles    = 5000
	maxReadSize = 2 << 20
)

// Project is one AnsibleProject of a workspace.
type Project struct {
	W      *workspace.Workspace
	R      *workspace.Resource
	Source string // repo | folder
	Repo   string // GitRemote (source repo)
	Root   string // absolute folder
}

// Open resolves the project named name.
func Open(w *workspace.Workspace, name string) (*Project, error) {
	name = strings.TrimPrefix(strings.TrimPrefix(name, "ansible/"), Kind+"/")
	r := w.Find(Kind, name)
	if r == nil {
		var names []string
		for _, x := range Resources(w) {
			names = append(names, x.Name)
		}
		if len(names) == 0 {
			return nil, errors.New("this workspace has no Ansible projects yet: add one on the Ansible page (a folder or a Git repository)")
		}
		return nil, fmt.Errorf("Ansible project %q not found; projects: %s", name, strings.Join(names, ", "))
	}
	p := &Project{W: w, R: r, Source: r.Str("source"), Repo: r.Str("repo")}
	if p.Source == "" {
		p.Source = "folder"
		if p.Repo != "" {
			p.Source = "repo"
		}
	}
	sub := strings.TrimSpace(r.Str("path"))
	switch p.Source {
	case "repo":
		if p.Repo == "" {
			return nil, fmt.Errorf("%s: choose the Git repository that holds it", r.ID())
		}
		if w.Find("GitRemote", p.Repo) == nil {
			return nil, fmt.Errorf("%s: Git repository %q not found", r.ID(), p.Repo)
		}
		rel, err := cleanRel(sub)
		if err != nil {
			return nil, fmt.Errorf("%s: path: %w", r.ID(), err)
		}
		p.Root = filepath.Join(w.RepoDir(p.Repo), filepath.FromSlash(rel))
	case "folder":
		switch {
		case sub == "":
			p.Root = filepath.Join(w.Path, "ansible", r.Name)
		case strings.HasPrefix(sub, "~/") || sub == "~":
			home, _ := os.UserHomeDir()
			p.Root = filepath.Join(home, strings.TrimPrefix(sub, "~"))
		case filepath.IsAbs(sub):
			p.Root = filepath.Clean(sub)
		default:
			rel, err := cleanRel(sub)
			if err != nil {
				return nil, fmt.Errorf("%s: path: %w", r.ID(), err)
			}
			p.Root = filepath.Join(w.Path, filepath.FromSlash(rel))
		}
	default:
		return nil, fmt.Errorf("%s: source must be repo or folder", r.ID())
	}
	return p, nil
}

// Resources lists the workspace's Ansible projects.
func Resources(w *workspace.Workspace) []*workspace.Resource {
	var out []*workspace.Resource
	for _, r := range w.Resources {
		if r.Kind == Kind {
			out = append(out, r)
		}
	}
	return out
}

// Name is the project's resource name.
func (p *Project) Name() string { return p.R.Name }

// Key identifies the project across workspaces.
func (p *Project) Key() string { return p.W.Name() + "/" + p.R.Name }

// External reports whether the project's folder is outside the workspace
// folder (a folder elsewhere on this computer).
func (p *Project) External() bool {
	ws, err1 := filepath.EvalSymlinks(p.W.Path)
	root, err2 := filepath.EvalSymlinks(p.Root)
	if err1 != nil {
		ws = filepath.Clean(p.W.Path)
	}
	if err2 != nil {
		root = filepath.Clean(p.Root)
	}
	return root != ws && !strings.HasPrefix(root, ws+string(filepath.Separator))
}

// Exists reports whether the folder exists.
func (p *Project) Exists() bool {
	st, err := os.Stat(p.Root)
	return err == nil && st.IsDir()
}

// DefaultInventory is the inventory runs use when none is chosen: the
// project's setting, else ansible.cfg's (ansible finds it itself).
func (p *Project) DefaultInventory() string { return p.R.Str("inventory") }

func cleanRel(p string) (string, error) {
	p = strings.TrimSpace(filepath.ToSlash(p))
	if p == "" || p == "." || p == "/" {
		return ".", nil
	}
	if filepath.IsAbs(p) || strings.HasPrefix(p, "/") {
		return "", errors.New("must be relative")
	}
	clean := filepath.ToSlash(filepath.Clean(p))
	for _, seg := range strings.Split(clean, "/") {
		if seg == ".." {
			return "", errors.New("must stay inside the folder")
		}
		if strings.EqualFold(seg, ".git") {
			return "", errors.New(".git is not accessible")
		}
	}
	return clean, nil
}

// Path resolves a file path relative to the project root, refusing paths
// that leave it (also through symbolic links) or enter .git.
func (p *Project) Path(rel string) (string, error) {
	clean, err := cleanRel(rel)
	if err != nil {
		return "", fmt.Errorf("%s: %w", rel, err)
	}
	abs := filepath.Join(p.Root, filepath.FromSlash(clean))
	root, err := filepath.EvalSymlinks(p.Root)
	if err != nil {
		return "", fmt.Errorf("the project folder %s does not exist", p.Root)
	}
	check := abs
	for {
		if resolved, err := filepath.EvalSymlinks(check); err == nil {
			if resolved != root && !strings.HasPrefix(resolved, root+string(filepath.Separator)) {
				return "", fmt.Errorf("%s leaves the project folder", rel)
			}
			break
		}
		parent := filepath.Dir(check)
		if parent == check {
			break
		}
		check = parent
	}
	return abs, nil
}

// ---------------------------------------------------------------------------
// Files

// File types.
const (
	TypePlaybook     = "playbook"
	TypeInventory    = "inventory"
	TypeVars         = "vars"
	TypeRole         = "role"
	TypeTemplate     = "template"
	TypeConfig       = "config"
	TypeRequirements = "requirements"
	TypeFile         = "file"
)

// File is one file of a project.
type File struct {
	Path      string    `json:"path"`
	Size      int64     `json:"size"`
	Modified  time.Time `json:"modified"`
	Type      string    `json:"type"`
	Encrypted bool      `json:"encrypted,omitempty"` // a whole-file Ansible Vault
	VaultID   string    `json:"vaultId,omitempty"`
	Inline    int       `json:"inline,omitempty"` // inline !vault values
}

var skipDirs = map[string]bool{".git": true, "node_modules": true, ".venv": true, "venv": true, "__pycache__": true, ".tox": true, ".mypy_cache": true, ".pytest_cache": true}

// Files lists the project's files (up to 5000), classified.
func (p *Project) Files() ([]File, bool, error) {
	if !p.Exists() {
		return nil, false, fmt.Errorf("the project folder %s does not exist", p.Root)
	}
	var out []File
	truncated := false
	err := filepath.WalkDir(p.Root, func(abs string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if abs != p.Root && skipDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		if len(out) >= maxFiles {
			truncated = true
			return filepath.SkipAll
		}
		rel, _ := filepath.Rel(p.Root, abs)
		rel = filepath.ToSlash(rel)
		info, err := d.Info()
		if err != nil {
			return nil
		}
		f := File{Path: rel, Size: info.Size(), Modified: info.ModTime().UTC()}
		f.Type = classify(rel)
		if info.Size() <= maxReadSize && isTextName(rel) {
			if b, err := os.ReadFile(abs); err == nil {
				sniff(&f, b)
			}
		}
		out = append(out, f)
		return nil
	})
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, truncated, err
}

func isYAML(p string) bool {
	ext := strings.ToLower(filepath.Ext(p))
	return ext == ".yml" || ext == ".yaml"
}

func isTextName(p string) bool {
	switch strings.ToLower(filepath.Ext(p)) {
	case ".png", ".jpg", ".jpeg", ".gif", ".ico", ".zip", ".gz", ".tgz", ".tar", ".bz2", ".xz", ".so", ".pyc", ".bin", ".deb", ".rpm", ".pdf", ".woff", ".woff2":
		return false
	}
	return true
}

// classify guesses a file's role from where it is.
func classify(rel string) string {
	base := strings.ToLower(filepath.Base(rel))
	parts := strings.Split(rel, "/")
	dirs := parts[:len(parts)-1]
	in := func(name string) bool {
		for _, d := range dirs {
			if d == name {
				return true
			}
		}
		return false
	}
	switch {
	case base == "ansible.cfg":
		return TypeConfig
	case strings.HasPrefix(base, "requirements") && isYAML(base):
		return TypeRequirements
	case in("group_vars") || in("host_vars"):
		return TypeVars
	case in("roles"):
		if in("templates") {
			return TypeTemplate
		}
		return TypeRole
	case in("inventory") || in("inventories"):
		return TypeInventory
	case base == "hosts" || strings.HasPrefix(base, "hosts.") || base == "inventory" || strings.HasPrefix(base, "inventory."):
		return TypeInventory
	case in("vars") || in("defaults"):
		return TypeVars
	case in("templates") || strings.HasSuffix(base, ".j2"):
		return TypeTemplate
	}
	return TypeFile
}

var playRe = regexp.MustCompile(`(?m)^-\s+(?:hosts|import_playbook|ansible\.builtin\.import_playbook)\s*:|^\s{2,4}(?:hosts|import_playbook|ansible\.builtin\.import_playbook)\s*:`)

// sniff refines a file's type from its content: vault files, inline vault
// values, playbooks.
func sniff(f *File, b []byte) {
	if IsEncrypted(b) {
		f.Encrypted = true
		if h, err := ParseHeader(string(b)); err == nil {
			f.VaultID = h.ID
		}
		return
	}
	if bytes.Contains(b, []byte("!vault")) {
		f.Inline = len(FindInline(string(b)))
	}
	if f.Type == TypeFile && isYAML(f.Path) && playRe.Match(b) && isPlaybook(b) {
		f.Type = TypePlaybook
	}
}

func isPlaybook(b []byte) bool {
	var plays []map[string]any
	if err := yaml.Unmarshal(b, &plays); err != nil || len(plays) == 0 {
		return false
	}
	for _, p := range plays {
		for _, k := range []string{"hosts", "import_playbook", "ansible.builtin.import_playbook"} {
			if _, ok := p[k]; ok {
				return true
			}
		}
	}
	return false
}

// FileContent is a file as read.
type FileContent struct {
	Path      string   `json:"path"`
	Text      string   `json:"text"`
	Size      int64    `json:"size"`
	Binary    bool     `json:"binary,omitempty"`
	Truncated bool     `json:"truncated,omitempty"`
	Encrypted bool     `json:"encrypted,omitempty"`
	VaultID   string   `json:"vaultId,omitempty"`
	Inline    []Inline `json:"inline,omitempty"`
}

// Read returns a file's content (the vault text of an encrypted file).
func (p *Project) Read(rel string) (FileContent, error) {
	abs, err := p.Path(rel)
	if err != nil {
		return FileContent{}, err
	}
	st, err := os.Stat(abs)
	if err != nil {
		return FileContent{}, fmt.Errorf("%s: %w", rel, errors.Unwrap(err))
	}
	if st.IsDir() {
		return FileContent{}, fmt.Errorf("%s is a folder", rel)
	}
	f, err := os.Open(abs)
	if err != nil {
		return FileContent{}, err
	}
	defer f.Close()
	buf := make([]byte, maxReadSize+1)
	n, _ := f.Read(buf)
	for n < len(buf) {
		m, err := f.Read(buf[n:])
		n += m
		if err != nil || m == 0 {
			break
		}
	}
	b := buf[:n]
	fc := FileContent{Path: filepath.ToSlash(rel), Size: st.Size()}
	if len(b) > maxReadSize {
		b, fc.Truncated = b[:maxReadSize], true
	}
	if bytes.IndexByte(b, 0) >= 0 {
		fc.Binary = true
		return fc, nil
	}
	fc.Text = string(b)
	if IsEncrypted(b) {
		fc.Encrypted = true
		if h, err := ParseHeader(fc.Text); err == nil {
			fc.VaultID = h.ID
		}
	} else if bytes.Contains(b, []byte("!vault")) {
		fc.Inline = FindInline(fc.Text)
	}
	return fc, nil
}

// ReadText returns a whole text file (refusing binary or oversized ones).
func (p *Project) ReadText(rel string) (string, error) {
	fc, err := p.Read(rel)
	if err != nil {
		return "", err
	}
	if fc.Binary || fc.Truncated {
		return "", fmt.Errorf("%s is not a text file of at most 2 MB", rel)
	}
	return fc.Text, nil
}

// Write writes a file atomically (creating folders), keeping its mode.
func (p *Project) Write(rel, content string) error {
	abs, err := p.Path(rel)
	if err != nil {
		return err
	}
	if !p.Exists() {
		return fmt.Errorf("the project folder %s does not exist", p.Root)
	}
	if st, err := os.Stat(abs); err == nil && st.IsDir() {
		return fmt.Errorf("%s is a folder", rel)
	}
	mode := os.FileMode(0o644)
	if st, err := os.Stat(abs); err == nil {
		mode = st.Mode().Perm()
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return err
	}
	tmp := abs + ".opsdeck-tmp"
	if err := os.WriteFile(tmp, []byte(content), mode); err != nil {
		return err
	}
	if err := os.Rename(tmp, abs); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// Delete removes a file.
func (p *Project) Delete(rel string) error {
	abs, err := p.Path(rel)
	if err != nil {
		return err
	}
	st, err := os.Lstat(abs)
	if err != nil {
		return fmt.Errorf("%s not found", rel)
	}
	if st.IsDir() {
		return fmt.Errorf("%s is a folder", rel)
	}
	return os.Remove(abs)
}

// Rename moves a file inside the project.
func (p *Project) Rename(from, to string) error {
	a, err := p.Path(from)
	if err != nil {
		return err
	}
	b, err := p.Path(to)
	if err != nil {
		return err
	}
	if _, err := os.Stat(b); err == nil {
		return fmt.Errorf("%s already exists", to)
	}
	if err := os.MkdirAll(filepath.Dir(b), 0o755); err != nil {
		return err
	}
	return os.Rename(a, b)
}

// Match is a search hit.
type Match struct {
	Path string `json:"path"`
	Line int    `json:"line"`
	Text string `json:"text"`
}

// Search finds lines matching query (a regular expression when re) in the
// project's text files; vault files are skipped.
func (p *Project) Search(query string, re bool, max int) ([]Match, error) {
	if query == "" {
		return nil, errors.New("query is empty")
	}
	var rx *regexp.Regexp
	var err error
	if re {
		if rx, err = regexp.Compile(query); err != nil {
			return nil, err
		}
	} else if rx, err = regexp.Compile("(?i)" + regexp.QuoteMeta(query)); err != nil {
		return nil, err
	}
	files, _, err := p.Files()
	if err != nil {
		return nil, err
	}
	var out []Match
	for _, f := range files {
		if f.Encrypted || f.Size > maxReadSize || !isTextName(f.Path) {
			continue
		}
		b, err := os.ReadFile(filepath.Join(p.Root, filepath.FromSlash(f.Path)))
		if err != nil || bytes.IndexByte(b, 0) >= 0 {
			continue
		}
		sc := bufio.NewScanner(bytes.NewReader(b))
		sc.Buffer(make([]byte, 64*1024), 1<<20)
		n := 0
		for sc.Scan() {
			n++
			if rx.MatchString(sc.Text()) {
				t := sc.Text()
				if len(t) > 300 {
					t = t[:300] + "…"
				}
				out = append(out, Match{Path: f.Path, Line: n, Text: t})
				if len(out) >= max {
					return out, nil
				}
			}
		}
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// Overview

// Inventories lists the inventories found in the project: ansible.cfg's,
// files named hosts/inventory and the folders under inventories/.
func (p *Project) Inventories(files []File) []string {
	seen := map[string]bool{}
	var out []string
	add := func(s string) {
		s = strings.TrimSuffix(filepath.ToSlash(s), "/")
		if s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	if d := p.DefaultInventory(); d != "" {
		add(d)
	}
	for _, inv := range cfgInventories(p.Root) {
		add(inv)
	}
	for _, f := range files {
		if f.Type != TypeInventory {
			continue
		}
		parts := strings.Split(f.Path, "/")
		base := strings.ToLower(parts[len(parts)-1])
		switch {
		case len(parts) >= 3 && (parts[0] == "inventories" || parts[0] == "inventory") && !contains(parts, "group_vars") && !contains(parts, "host_vars"):
			add(parts[0] + "/" + parts[1]) // a folder per environment
		case len(parts) == 2 && (parts[0] == "inventories" || parts[0] == "inventory"):
			add(f.Path)
		case len(parts) <= 2 && (base == "hosts" || strings.HasPrefix(base, "hosts.") || base == "inventory" || strings.HasPrefix(base, "inventory.")):
			add(f.Path)
		}
	}
	return out
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

var cfgInvRe = regexp.MustCompile(`(?m)^\s*inventory\s*=\s*(.+?)\s*$`)

// cfgInventories reads the inventory setting of the project's ansible.cfg.
func cfgInventories(root string) []string {
	b, err := os.ReadFile(filepath.Join(root, "ansible.cfg"))
	if err != nil {
		return nil
	}
	var out []string
	if m := cfgInvRe.FindSubmatch(b); m != nil {
		for _, s := range strings.Split(string(m[1]), ",") {
			s = strings.TrimSpace(s)
			if s != "" && !filepath.IsAbs(s) && !strings.HasPrefix(s, "~") {
				out = append(out, strings.TrimPrefix(s, "./"))
			}
		}
	}
	return out
}

// Summary describes a project for lists.
type Summary struct {
	Name         string   `json:"name"`
	Environment  string   `json:"environment,omitempty"`
	Description  string   `json:"description,omitempty"`
	Source       string   `json:"source"`
	Repo         string   `json:"repo,omitempty"`
	Path         string   `json:"path,omitempty"`
	Root         string   `json:"root"`
	External     bool     `json:"external"`
	Exists       bool     `json:"exists"`
	Via          string   `json:"via,omitempty"`
	Inventory    string   `json:"inventory,omitempty"`
	Inventories  []string `json:"inventories"`
	Playbooks    []string `json:"playbooks"`
	VaultFiles   int      `json:"vaultFiles"`
	InlineVars   int      `json:"inlineVars"`
	VaultIDs     []string `json:"vaultIds"`
	Unlocked     []string `json:"unlocked"` // vault ids with a password at hand
	Saved        []string `json:"saved"`    // vault ids with a password saved in the vault
	Config       bool     `json:"config"`
	Requirements []string `json:"requirements"`
	Files        int      `json:"files"`
	Error        string   `json:"error,omitempty"`
}

// Summarize lists what a project holds.
func (p *Project) Summarize() Summary {
	s := Summary{Name: p.R.Name, Environment: p.R.Environment, Description: p.R.Description, Source: p.Source, Repo: p.Repo,
		Path: p.R.Str("path"), Root: p.Root, External: p.External(), Exists: p.Exists(), Via: p.R.Via, Inventory: p.DefaultInventory(),
		Inventories: []string{}, Playbooks: []string{}, VaultIDs: []string{}, Unlocked: []string{}, Saved: []string{}, Requirements: []string{}}
	for id := range SavedRefs(p.R) {
		s.Saved = append(s.Saved, id)
	}
	sort.Strings(s.Saved)
	if !s.Exists {
		if p.Source == "repo" {
			s.Error = "the repository is not cloned yet (Repositories page) or has no folder " + firstNonEmpty(s.Path, ".")
		} else {
			s.Error = "the folder " + p.Root + " does not exist"
		}
		return s
	}
	files, _, err := p.Files()
	if err != nil {
		s.Error = err.Error()
		return s
	}
	s.Files = len(files)
	ids := map[string]bool{}
	for _, f := range files {
		switch {
		case f.Type == TypePlaybook:
			s.Playbooks = append(s.Playbooks, f.Path)
		case f.Type == TypeConfig && f.Path == "ansible.cfg":
			s.Config = true
		case f.Type == TypeRequirements:
			s.Requirements = append(s.Requirements, f.Path)
		}
		if f.Encrypted {
			s.VaultFiles++
			ids[VaultIDName(f.VaultID)] = true
		}
		if f.Inline > 0 {
			s.InlineVars += f.Inline
			if fc, err := p.Read(f.Path); err == nil {
				for _, in := range fc.Inline {
					ids[VaultIDName(in.VaultID)] = true
				}
			}
		}
	}
	for id := range ids {
		s.VaultIDs = append(s.VaultIDs, id)
	}
	sort.Strings(s.VaultIDs)
	s.Inventories = p.Inventories(files)
	return s
}

// VaultIDName names the default vault id "default", as ansible does.
func VaultIDName(id string) string {
	if id == "" {
		return "default"
	}
	return id
}

// SavedRefs are the vault passwords saved in opsdeck's vault, by vault id:
// spec.vaultPassword (default) and spec.vaultPasswords ("id=vault://…").
func SavedRefs(r *workspace.Resource) map[string]string {
	out := map[string]string{}
	if ref := r.Str("vaultPassword"); ref != "" {
		out["default"] = ref
	}
	for _, e := range r.List("vaultPasswords") {
		if id, ref, ok := strings.Cut(e, "="); ok && strings.HasPrefix(strings.TrimSpace(ref), "vault://") {
			out[VaultIDName(strings.TrimSpace(id))] = strings.TrimSpace(ref)
		}
	}
	return out
}

func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if s != "" {
			return s
		}
	}
	return ""
}

// Scaffold creates a new project folder with a minimal layout.
func (p *Project) Scaffold() error {
	if p.Exists() {
		return nil
	}
	if p.Source != "folder" {
		return errors.New("only folder projects can be created")
	}
	files := map[string]string{
		"ansible.cfg":                  "[defaults]\ninventory = inventory/hosts.yml\nroles_path = roles\nretry_files_enabled = False\nstdout_callback = default\n\n[ssh_connection]\npipelining = True\n",
		"inventory/hosts.yml":          "all:\n  hosts:\n    # web-1:\n    #   ansible_host: 10.0.0.10\n  children:\n    web:\n      hosts: {}\n",
		"inventory/group_vars/all.yml": "---\n# variables for every host\n",
		"site.yml":                     "---\n- name: Example play\n  hosts: all\n  gather_facts: true\n  tasks:\n    - name: Check connectivity\n      ansible.builtin.ping:\n",
		"requirements.yml":             "---\ncollections: []\nroles: []\n",
	}
	for rel, content := range files {
		abs := filepath.Join(p.Root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
			return err
		}
	}
	return os.MkdirAll(filepath.Join(p.Root, "roles"), 0o755)
}
