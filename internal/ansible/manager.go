package ansible

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"opsdeck/internal/connect"
	"opsdeck/internal/sysproc"
	"opsdeck/internal/workspace"
)

// Manager runs Ansible for every workspace and keeps the vault passwords
// entered in this process (in memory only, until opsdeck's vault locks).
type Manager struct {
	Hub *connect.Hub
	// Redact masks secrets in run logs (vault values and credential
	// patterns); the passwords of the keyring are masked too.
	Redact func(string) string
	// Executable is opsdeck itself (ssh ProxyCommand through a VPN).
	Executable string
	// Touch keeps opsdeck's vault from locking itself while a run is live.
	Touch func()

	mu      sync.Mutex
	keyring map[string]string // ws/project/vault-id -> password
	runs    map[string]*Run   // live and recently finished runs
	vers    map[string]versionInfo
}

type versionInfo struct {
	v   Version
	at  time.Time
	err error
}

// New returns a manager.
func New(h *connect.Hub) *Manager {
	return &Manager{Hub: h, keyring: map[string]string{}, runs: map[string]*Run{}, vers: map[string]versionInfo{}}
}

// NeedPassword is returned when a vault password is not at hand.
type NeedPassword struct {
	Project string
	VaultID string
	Wrong   bool // the passwords at hand did not open it
}

func (e *NeedPassword) Error() string {
	if e.Wrong {
		return fmt.Sprintf("the Ansible Vault password at hand for %s (vault id %s) does not open it; enter the right one", e.Project, VaultIDName(e.VaultID))
	}
	return fmt.Sprintf("the Ansible Vault password of %s (vault id %s) is needed", e.Project, VaultIDName(e.VaultID))
}

// IsNeedPassword reports whether err asks for a vault password.
func IsNeedPassword(err error) (*NeedPassword, bool) {
	var np *NeedPassword
	ok := errors.As(err, &np)
	return np, ok
}

// ---------------------------------------------------------------------------
// Keyring

func ringKey(p *Project, id string) string { return p.Key() + "/" + VaultIDName(id) }

// Remember keeps a password in memory for the project's vault id.
func (m *Manager) Remember(p *Project, id, password string) {
	m.mu.Lock()
	m.keyring[ringKey(p, id)] = password
	m.mu.Unlock()
}

// Forget drops the remembered passwords of a project (all ids when id is "").
func (m *Manager) Forget(p *Project, id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for k := range m.keyring {
		if (id == "" && strings.HasPrefix(k, p.Key()+"/")) || k == ringKey(p, id) {
			delete(m.keyring, k)
		}
	}
}

// ForgetAll drops every remembered password and stops the live runs
// (opsdeck's vault was locked: live access ends).
func (m *Manager) ForgetAll() {
	m.mu.Lock()
	m.keyring = map[string]string{}
	var live []*Run
	for _, r := range m.runs {
		live = append(live, r)
	}
	m.mu.Unlock()
	for _, r := range live {
		r.Cancel()
	}
}

// Secrets are the remembered passwords, for redaction.
func (m *Manager) Secrets() map[string]string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := map[string]string{}
	for k, v := range m.keyring {
		if len(v) >= 4 {
			out[v] = "ansible vault password " + k[strings.LastIndex(k, "/")+1:]
		}
	}
	return out
}

// Remembered lists the vault ids of a project with a password in memory.
func (m *Manager) Remembered(p *Project) []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []string
	for k := range m.keyring {
		if strings.HasPrefix(k, p.Key()+"/") {
			out = append(out, strings.TrimPrefix(k, p.Key()+"/"))
		}
	}
	sort.Strings(out)
	return out
}

type onceKey struct{}

type oncePasswords struct {
	project string
	pw      map[string]string
	parent  *oncePasswords
}

// WithPassword carries a password given for one action only (not kept).
func WithPassword(ctx context.Context, p *Project, id, password string) context.Context {
	parent, _ := ctx.Value(onceKey{}).(*oncePasswords)
	return context.WithValue(ctx, onceKey{}, &oncePasswords{project: p.Key(), pw: map[string]string{VaultIDName(id): password}, parent: parent})
}

// Passwords returns the passwords at hand for a project by vault id: given
// for this action, remembered, or saved in opsdeck's vault (the actor in
// ctx decides whether a saved one may be used).
func (m *Manager) Passwords(ctx context.Context, p *Project) map[string]string {
	out := map[string]string{}
	for id, ref := range SavedRefs(p.R) {
		if m.Hub == nil {
			break
		}
		it, field, err := m.Hub.Credential(ctx, p.W, ref)
		if err != nil {
			continue
		}
		v := ""
		if field != "" {
			v = it.Get(field)
		}
		for _, f := range []string{"password", "token", "content"} {
			if v == "" {
				v = it.Get(f)
			}
		}
		if v = strings.TrimRight(v, "\r\n"); v != "" {
			out[id] = v
		}
	}
	m.mu.Lock()
	for k, v := range m.keyring {
		if strings.HasPrefix(k, p.Key()+"/") {
			out[strings.TrimPrefix(k, p.Key()+"/")] = v
		}
	}
	m.mu.Unlock()
	// given for this action: the newest, so they win (outermost last)
	var once []*oncePasswords
	for o, _ := ctx.Value(onceKey{}).(*oncePasswords); o != nil; o = o.parent {
		once = append(once, o)
	}
	for i := len(once) - 1; i >= 0; i-- {
		if once[i].project == p.Key() {
			for id, v := range once[i].pw {
				out[id] = v
			}
		}
	}
	return out
}

// HasPassword reports whether a password for id is at hand.
func (m *Manager) HasPassword(ctx context.Context, p *Project, id string) bool {
	_, ok := m.Passwords(ctx, p)[VaultIDName(id)]
	return ok
}

// order lists passwords to try for vault text: the one of its own vault id
// first, then the others (ansible also tries every password it has).
func order(pw map[string]string, id string) []string {
	var out []string
	if v, ok := pw[VaultIDName(id)]; ok {
		out = append(out, v)
	}
	ids := make([]string, 0, len(pw))
	for k := range pw {
		ids = append(ids, k)
	}
	sort.Strings(ids)
	for _, k := range ids {
		if k != VaultIDName(id) {
			out = append(out, pw[k])
		}
	}
	return out
}

// Open decrypts vault text with the passwords at hand.
func (m *Manager) Open(ctx context.Context, p *Project, text string) ([]byte, error) {
	h, err := ParseHeader(text)
	if err != nil {
		return nil, err
	}
	tried := order(m.Passwords(ctx, p), h.ID)
	for _, pw := range tried {
		if b, err := Decrypt(text, pw); err == nil {
			return b, nil
		}
	}
	return nil, &NeedPassword{Project: p.R.Name, VaultID: h.ID, Wrong: len(tried) > 0}
}

// CheckPassword verifies password for a vault id against the project's
// encrypted files and values. ok is false when nothing of that id exists to
// check it against (it is then accepted as given).
func CheckPassword(p *Project, id, password string) (checked bool, err error) {
	files, _, err := p.Files()
	if err != nil {
		return false, err
	}
	id = VaultIDName(id)
	for _, f := range files {
		if f.Encrypted && VaultIDName(f.VaultID) == id {
			text, err := p.ReadText(f.Path)
			if err != nil {
				continue
			}
			_, err = Decrypt(text, password)
			return true, err
		}
	}
	for _, f := range files {
		if f.Inline == 0 {
			continue
		}
		fc, err := p.Read(f.Path)
		if err != nil {
			continue
		}
		for _, in := range fc.Inline {
			if VaultIDName(in.VaultID) == id {
				_, err := in.DecryptValue(password)
				return true, err
			}
		}
	}
	return false, nil
}

// ---------------------------------------------------------------------------
// Vault operations on files

// View returns the plaintext of a file: a vault file decrypted, or a YAML
// file with its inline values decrypted.
func (m *Manager) View(ctx context.Context, p *Project, rel string) (string, error) {
	fc, err := p.Read(rel)
	if err != nil {
		return "", err
	}
	if fc.Binary || fc.Truncated {
		return "", fmt.Errorf("%s is not a text file of at most 2 MB", rel)
	}
	if fc.Encrypted {
		b, err := m.Open(ctx, p, fc.Text)
		return string(b), err
	}
	if len(fc.Inline) == 0 {
		return "", fmt.Errorf("%s is not encrypted", rel)
	}
	pw := m.Passwords(ctx, p)
	var need *NeedPassword
	out, _ := RevealInline(fc.Text, func(in Inline) (string, error) {
		for _, v := range order(pw, in.VaultID) {
			if s, err := in.DecryptValue(v); err == nil {
				return s, nil
			}
		}
		if need == nil {
			need = &NeedPassword{Project: p.R.Name, VaultID: in.VaultID}
		}
		return "", need
	})
	if need != nil {
		return "", need
	}
	return out, nil
}

// encryptFor encrypts plaintext with the password of id.
func (m *Manager) encryptFor(ctx context.Context, p *Project, id string, plain []byte) (string, error) {
	pw, ok := m.Passwords(ctx, p)[VaultIDName(id)]
	if !ok {
		return "", &NeedPassword{Project: p.R.Name, VaultID: id}
	}
	if id == "default" {
		id = ""
	}
	return Encrypt(plain, pw, id)
}

// SaveEncrypted writes plaintext into a vault file, encrypted again with
// the file's own vault id (or id for a new file).
func (m *Manager) SaveEncrypted(ctx context.Context, p *Project, rel, plain, id string) error {
	if fc, err := p.Read(rel); err == nil {
		if !fc.Encrypted {
			return fmt.Errorf("%s is not a vault file", rel)
		}
		if _, err := m.Open(ctx, p, fc.Text); err != nil { // the password must open the current file
			return err
		}
		id = fc.VaultID
	}
	text, err := m.encryptFor(ctx, p, id, []byte(plain))
	if err != nil {
		return err
	}
	return p.Write(rel, text)
}

// EncryptFile encrypts a plain file in place.
func (m *Manager) EncryptFile(ctx context.Context, p *Project, rel, id string) error {
	fc, err := p.Read(rel)
	if err != nil {
		return err
	}
	if fc.Encrypted {
		return fmt.Errorf("%s is already encrypted", rel)
	}
	if fc.Binary || fc.Truncated {
		return fmt.Errorf("%s is not a text file of at most 2 MB", rel)
	}
	text, err := m.encryptFor(ctx, p, id, []byte(fc.Text))
	if err != nil {
		return err
	}
	return p.Write(rel, text)
}

// DecryptFile writes a vault file back as plain text.
func (m *Manager) DecryptFile(ctx context.Context, p *Project, rel string) error {
	fc, err := p.Read(rel)
	if err != nil {
		return err
	}
	if !fc.Encrypted {
		return fmt.Errorf("%s is not encrypted", rel)
	}
	b, err := m.Open(ctx, p, fc.Text)
	if err != nil {
		return err
	}
	return p.Write(rel, string(b))
}

// Rekey encrypts a vault file with a new password (and vault id).
func (m *Manager) Rekey(ctx context.Context, p *Project, rel, newPassword, newID string) error {
	fc, err := p.Read(rel)
	if err != nil {
		return err
	}
	if !fc.Encrypted {
		return fmt.Errorf("%s is not encrypted", rel)
	}
	b, err := m.Open(ctx, p, fc.Text)
	if err != nil {
		return err
	}
	if newID == "default" {
		newID = ""
	}
	text, err := Encrypt(b, newPassword, newID)
	if err != nil {
		return err
	}
	return p.Write(rel, text)
}

// SetVar sets a top-level variable in a vars file: in a vault file the
// plaintext is changed and encrypted again; in a plain YAML file the value
// is written as an inline !vault value.
func (m *Manager) SetVar(ctx context.Context, p *Project, rel, key, value, id string) (created bool, err error) {
	fc, err := p.Read(rel)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		created, fc = true, FileContent{}
	case err != nil:
		return false, err
	case fc.Binary || fc.Truncated:
		return false, fmt.Errorf("%s is not a text file of at most 2 MB", rel)
	}
	if fc.Encrypted {
		plain, err := m.Open(ctx, p, fc.Text)
		if err != nil {
			return false, err
		}
		next, err := SetYAMLKey(string(plain), key, YAMLString(value))
		if err != nil {
			return false, err
		}
		text, err := m.encryptFor(ctx, p, fc.VaultID, []byte(next))
		if err != nil {
			return false, err
		}
		return false, p.Write(rel, text)
	}
	if !isYAML(rel) {
		return false, fmt.Errorf("%s is not a YAML file", rel)
	}
	pw, ok := m.Passwords(ctx, p)[VaultIDName(id)]
	if !ok {
		return false, &NeedPassword{Project: p.R.Name, VaultID: id}
	}
	vid := id
	if vid == "default" {
		vid = ""
	}
	block, err := EncryptString("", value, pw, vid, 0)
	if err != nil {
		return false, err
	}
	next, err := SetYAMLKey(fc.Text, key, strings.TrimRight(block, "\n"))
	if err != nil {
		return false, err
	}
	return created, p.Write(rel, next)
}

// ---------------------------------------------------------------------------
// Installed ansible

// Version is the installed ansible.
type Version struct {
	Installed bool   `json:"installed"`
	Version   string `json:"version,omitempty"` // e.g. "core 2.17.1"
	Path      string `json:"path,omitempty"`
	Python    string `json:"python,omitempty"`
	Lint      bool   `json:"lint"` // ansible-lint is installed
	Error     string `json:"error,omitempty"`
}

// binDir is the project's ansible bin folder (spec.bin), or "".
func binDir(p *Project) string {
	b := strings.TrimSpace(p.R.Str("bin"))
	if strings.HasPrefix(b, "~/") {
		home, _ := os.UserHomeDir()
		b = filepath.Join(home, b[2:])
	}
	return b
}

// lookPath finds an ansible program for the project.
func lookPath(p *Project, prog string) (string, error) {
	if d := binDir(p); d != "" {
		c := filepath.Join(d, prog)
		if st, err := os.Stat(c); err == nil && !st.IsDir() {
			return c, nil
		}
		return "", fmt.Errorf("%s not found in %s", prog, d)
	}
	if c, err := exec.LookPath(prog); err == nil {
		return c, nil
	}
	// common places for pipx / pip --user installs
	home, _ := os.UserHomeDir()
	for _, d := range []string{filepath.Join(home, ".local", "bin"), "/opt/homebrew/bin", "/usr/local/bin"} {
		c := filepath.Join(d, prog)
		if st, err := os.Stat(c); err == nil && !st.IsDir() {
			return c, nil
		}
	}
	return "", fmt.Errorf("%s is not installed on this computer (pipx install --include-deps ansible, or pip install ansible-core); or set the project's ansible folder", prog)
}

// Detect reports the ansible used by a project (cached for a minute).
func (m *Manager) Detect(ctx context.Context, p *Project) Version {
	key := binDir(p)
	m.mu.Lock()
	if v, ok := m.vers[key]; ok && time.Since(v.at) < time.Minute {
		m.mu.Unlock()
		return v.v
	}
	m.mu.Unlock()
	v := Version{}
	path, err := lookPath(p, "ansible-playbook")
	if err != nil {
		v.Error = err.Error()
	} else {
		v.Path = path
		ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		cmd := sysproc.CommandContext(ctx, path, "--version")
		cmd.Env = baseEnv(p)
		out, err := cmd.CombinedOutput()
		if err != nil {
			v.Error = strings.TrimSpace(firstNonEmpty(string(out), err.Error()))
		} else {
			v.Installed = true
			lines := strings.Split(string(out), "\n")
			if f := strings.TrimSpace(lines[0]); strings.Contains(f, "[") {
				v.Version = strings.Trim(f[strings.Index(f, "["):], "[]")
			} else {
				v.Version = strings.TrimSpace(strings.TrimPrefix(f, "ansible-playbook"))
			}
			for _, l := range lines {
				if t := strings.TrimSpace(l); strings.HasPrefix(t, "python version") {
					v.Python = strings.TrimSpace(strings.TrimPrefix(t, "python version ="))
					if i := strings.Index(v.Python, " ("); i > 0 {
						v.Python = v.Python[:i]
					}
				}
			}
		}
	}
	if _, err := lookPath(p, "ansible-lint"); err == nil {
		v.Lint = true
	}
	m.mu.Lock()
	m.vers[key] = versionInfo{v: v, at: time.Now()}
	m.mu.Unlock()
	return v
}

// baseEnv is the environment of ansible processes: opsdeck's own, without
// OPSDECK_* variables, the project's bin folder first in PATH, colours on
// and no retry files.
func baseEnv(p *Project) []string {
	var out []string
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(k, "OPSDECK_") || k == "ANSIBLE_VAULT_PASSWORD_FILE" || k == "ANSIBLE_VAULT_IDENTITY_LIST" {
			continue
		}
		if k == "PATH" && binDir(p) != "" {
			kv = "PATH=" + binDir(p) + string(os.PathListSeparator) + strings.TrimPrefix(kv, "PATH=")
		}
		out = append(out, kv)
	}
	return append(out, "ANSIBLE_FORCE_COLOR=1", "PY_COLORS=1", "PYTHONUNBUFFERED=1", "ANSIBLE_RETRY_FILES_ENABLED=0", "ANSIBLE_NOCOWS=1")
}

// Projects lists the summaries of a workspace's projects.
func (m *Manager) Projects(ctx context.Context, w *workspace.Workspace) []Summary {
	out := []Summary{}
	for _, r := range Resources(w) {
		p, err := Open(w, r.Name)
		if err != nil {
			out = append(out, Summary{Name: r.Name, Environment: r.Environment, Source: r.Str("source"), Repo: r.Str("repo"), Error: err.Error(),
				Inventories: []string{}, Playbooks: []string{}, VaultIDs: []string{}, Unlocked: []string{}, Saved: []string{}, Requirements: []string{}})
			continue
		}
		s := p.Summarize()
		pw := m.Passwords(ctx, p)
		for id := range pw {
			s.Unlocked = append(s.Unlocked, id)
		}
		sort.Strings(s.Unlocked)
		out = append(out, s)
	}
	return out
}
