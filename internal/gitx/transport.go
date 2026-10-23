// Package gitx keeps working copies of a workspace's Git repositories
// (GitRemote resources) in <workspace>/repos/<name> and talks to their
// hosting (GitHub, GitLab) for merge requests.
//
// It runs the system git with an isolated configuration: the user's and the
// system's git config are ignored, credentials come from the vault and reach
// git only through its environment (an HTTP header scoped to the remote's
// origin, or an in-memory ssh-agent holding the deploy key) — never on the
// command line, in .git/config or in a credential helper. Repositories behind
// a VPN or a jump host are reached through a throwaway SOCKS proxy on
// 127.0.0.1 that only this git process knows the password of.
package gitx

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"

	"opsdeck/internal/config"
	"opsdeck/internal/connect"
	"opsdeck/internal/sysproc"
	"opsdeck/internal/vault"
	"opsdeck/internal/vpn"
	"opsdeck/internal/workspace"
)

// Manager gives access to the working copies of all workspaces.
type Manager struct {
	Hub *connect.Hub
	// Executable is opsdeck's binary, used as ssh's ProxyCommand when an SSH
	// remote is reached through a VPN or jump host (default: os.Executable).
	Executable string
	// Identity returns the configured commit author; empty values fall back
	// to the token owner on the hosting service.
	Identity func() (name, email string)
	// Connections returns the connected GitHub/GitLab accounts; Vault
	// renews their OAuth tokens.
	Connections func() []config.GitConnection
	Vault       *vault.Vault

	mu    sync.Mutex
	locks map[string]*sync.Mutex
	ops   map[string]string    // ws/repo -> operation in progress
	kinds map[string][2]string // origin|setting -> kind, api base
	users map[string]*User     // ws/repo -> token owner
}

func New(h *connect.Hub) *Manager {
	return &Manager{Hub: h, locks: map[string]*sync.Mutex{}, ops: map[string]string{}, kinds: map[string][2]string{}, users: map[string]*User{}}
}

// lock serialises operations on one working copy and records which one is
// running (shown by Status, which does not wait for it).
func (m *Manager) lock(key, op string) func() {
	m.mu.Lock()
	l := m.locks[key]
	if l == nil {
		l = &sync.Mutex{}
		m.locks[key] = l
	}
	m.mu.Unlock()
	l.Lock()
	m.mu.Lock()
	m.ops[key] = op
	m.mu.Unlock()
	return func() {
		m.mu.Lock()
		delete(m.ops, key)
		m.mu.Unlock()
		l.Unlock()
	}
}

func (m *Manager) running(key string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.ops[key]
}

// Repo is one GitRemote of a workspace and its working copy.
type Repo struct {
	m      *Manager
	W      *workspace.Workspace
	R      *workspace.Resource
	Dir    string
	Remote Remote
	Conn   *config.GitConnection // connector serving it, if any
}

// Open returns the repository named name (a GitRemote resource).
func (m *Manager) Open(w *workspace.Workspace, name string) (*Repo, error) {
	r := w.Find("GitRemote", strings.TrimPrefix(strings.TrimPrefix(name, "GitRemote/"), "git/"))
	if r == nil {
		return nil, fmt.Errorf("git repository %q not found in the workspace", name)
	}
	rem, err := ParseRemote(r.Str("url"))
	if err != nil {
		return nil, err
	}
	conn, err := m.connection(r, rem)
	if err != nil {
		return nil, err
	}
	return &Repo{m: m, W: w, R: r, Dir: w.RepoDir(r.Name), Remote: rem, Conn: conn}, nil
}

// Credentials lists the vault references a repository uses (its token or
// deploy key and everything on the way to it).
func (m *Manager) Credentials(w *workspace.Workspace, r *workspace.Resource) []string {
	refs := m.Hub.Credentials(w, r)
	for _, f := range []string{"token", "deployKey"} {
		if ref := r.Str(f); ref != "" {
			refs = append(refs, ref)
		}
	}
	if r.Str("token") == "" {
		if rem, err := ParseRemote(r.Str("url")); err == nil {
			if c, _ := m.connection(r, rem); c != nil {
				refs = append(refs, c.Credential)
			}
		}
	}
	return refs
}

func (r *Repo) key() string { return r.W.Name() + "/" + r.R.Name }

// Cloned reports whether the working copy exists.
func (r *Repo) Cloned() bool {
	_, err := os.Stat(filepath.Join(r.Dir, ".git"))
	return err == nil
}

// Secret extracts what to authenticate with from a token or login item:
// the user name for HTTP Basic auth and the token or password.
func Secret(it vault.Item, field string) (user, secret string) {
	user = "oauth2" // accepted by GitLab and GitHub for tokens
	switch {
	case field != "":
		secret = it.Get(field)
	case it.Type == "login":
		if u := it.Get("username"); u != "" {
			user = u
		}
		secret = it.Get("password")
	default:
		secret = it.Get("token")
	}
	return user, secret
}

// AuthHeader is an HTTP Basic "Authorization: …" header line for git.
func AuthHeader(it vault.Item, field string) string {
	user, secret := Secret(it, field)
	if secret == "" {
		return ""
	}
	return "Authorization: Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"+secret))
}

// HasToken tells whether the repository has an access token (its own or
// its connector's).
func (r *Repo) HasToken() bool { return r.R.Str("token") != "" || r.Conn != nil }

// providerKind is the hosting software as far as known without asking.
func (r *Repo) providerKind() string {
	if p := r.R.Str("provider"); p != "" && p != "auto" {
		return p
	}
	if r.Conn != nil {
		return r.Conn.Provider
	}
	if strings.EqualFold(r.Remote.Host, "github.com") {
		return "github"
	}
	return ""
}

// token returns the repository's access token: its own, else its
// connector's ("" when it has none).
func (r *Repo) token(ctx context.Context) (user, secret string, err error) {
	ref := r.R.Str("token")
	if ref == "" {
		if r.Conn == nil {
			return "", "", nil
		}
		secret, err := r.m.ConnSecret(ctx, *r.Conn, r.W)
		return basicUser(r.Conn.Provider), secret, err
	}
	it, field, err := r.m.Hub.Credential(ctx, r.W, ref)
	if err != nil {
		return "", "", err
	}
	user, secret = Secret(it, field)
	if it.Type != "login" && r.providerKind() == "github" {
		user = basicUser("github")
	}
	return user, secret, nil
}

// caCert is the PEM of the server's private CA, if any.
func (r *Repo) caCert() string {
	if ca := strings.TrimSpace(r.R.Str("caCert")); ca != "" {
		return ca
	}
	if r.Conn != nil {
		return strings.TrimSpace(r.Conn.CACert)
	}
	return ""
}

// ----------------------------------------------------------------------------
// Running git

// baseEnv is the environment of every git process: no user/system config,
// no prompts, no credential helpers, stable output.
func baseEnv() []string {
	var env []string
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		switch uk := strings.ToUpper(k); {
		case strings.HasPrefix(uk, "GIT_"), uk == "SSH_ASKPASS", uk == "LC_ALL", uk == "LANG", uk == "LANGUAGE", uk == "EDITOR", uk == "VISUAL":
			continue
		}
		env = append(env, kv)
	}
	return append(env, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_TERMINAL_PROMPT=0",
		"GIT_ASKPASS=", "SSH_ASKPASS=", "LC_ALL=C", "GIT_EDITOR=true", "GIT_MERGE_AUTOEDIT=no")
}

// baseConfig hardens git against configuration found in the working copy.
var baseConfig = [][2]string{
	{"core.quotePath", "false"},
	{"credential.helper", ""},
	{"protocol.ext.allow", "never"},
	{"core.fsmonitor", "false"},
	{"core.hooksPath", os.DevNull},
	{"commit.gpgSign", "false"},
	{"tag.gpgSign", "false"},
	{"init.defaultBranch", "main"},
	{"advice.detachedHead", "false"},
	{"pull.rebase", "false"},
}

// env is a git process environment (and the helpers it needs).
type env struct {
	vars    []string
	cfg     [][2]string
	closers []func()
}

func (e *env) close() {
	for i := len(e.closers) - 1; i >= 0; i-- {
		e.closers[i]()
	}
	e.closers = nil
}

func (e *env) list() []string {
	out := append([]string{}, e.vars...)
	cfg := append(append([][2]string{}, baseConfig...), e.cfg...)
	out = append(out, fmt.Sprintf("GIT_CONFIG_COUNT=%d", len(cfg)))
	for i, kv := range cfg {
		out = append(out, fmt.Sprintf("GIT_CONFIG_KEY_%d=%s", i, kv[0]), fmt.Sprintf("GIT_CONFIG_VALUE_%d=%s", i, kv[1]))
	}
	return out
}

func localEnv() *env { return &env{vars: baseEnv()} }

func randHex(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// netEnv prepares git to talk to the remote: credentials, CA, proxy.
func (r *Repo) netEnv(ctx context.Context) (*env, error) {
	e := localEnv()
	ok := false
	defer func() {
		if !ok {
			e.close()
		}
	}()
	tmp, err := os.MkdirTemp("", "opsdeck-git-")
	if err != nil {
		return nil, err
	}
	e.closers = append(e.closers, func() { os.RemoveAll(tmp) })

	// Reaching the server through a VPN or a jump host.
	proxy := ""
	if r.R.Via != "" {
		dial, err := r.m.Hub.Dialer(ctx, r.W, r.R)
		if err != nil {
			return nil, err
		}
		user, pass := "git", randHex(16)
		srv, err := vpn.ServeSOCKSAuth(0, vpn.DialFunc(dial), user, pass)
		if err != nil {
			return nil, err
		}
		e.closers = append(e.closers, func() { srv.Close() })
		proxy = srv.Addr()
		// Proxies from the environment must not take over.
		vars := e.vars[:0]
		for _, kv := range e.vars {
			k, _, _ := strings.Cut(kv, "=")
			switch strings.ToLower(k) {
			case "http_proxy", "https_proxy", "all_proxy", "no_proxy":
				continue
			}
			vars = append(vars, kv)
		}
		e.vars = vars
		if !r.Remote.SSH {
			e.cfg = append(e.cfg, [2]string{"http.proxy", "socks5h://" + user + ":" + pass + "@" + proxy})
		} else {
			e.vars = append(e.vars, "OPSDECK_SOCKS_AUTH="+user+":"+pass)
		}
	}

	if r.Remote.SSH {
		sshCmd, err := r.sshCommand(ctx, e, tmp, proxy)
		if err != nil {
			return nil, err
		}
		e.vars = append(e.vars, "GIT_SSH_COMMAND="+sshCmd, "GIT_SSH_VARIANT=ssh")
	} else if r.Remote.Scheme == "https" || r.Remote.Scheme == "http" {
		origin := r.Remote.Origin()
		user, secret, err := r.token(ctx)
		if err != nil {
			return nil, err
		}
		if secret != "" {
			// Scoped to the remote's origin: never sent to another host.
			e.cfg = append(e.cfg, [2]string{"http." + origin + ".extraHeader",
				"Authorization: Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"+secret))})
		}
		if ca := r.caCert(); ca != "" {
			p := filepath.Join(tmp, "ca.pem")
			if err := os.WriteFile(p, []byte(ca+"\n"), 0o600); err != nil {
				return nil, err
			}
			e.cfg = append(e.cfg, [2]string{"http." + origin + ".sslCAInfo", p})
		}
	}
	ok = true
	return e, nil
}

func shq(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// sshCommand builds GIT_SSH_COMMAND: no prompts, host keys pinned on first
// use in the workspace, the deploy key from an in-memory agent.
func (r *Repo) sshCommand(ctx context.Context, e *env, tmp, proxy string) (string, error) {
	if err := os.MkdirAll(r.W.StateDir(), 0o700); err != nil {
		return "", err
	}
	known := filepath.ToSlash(filepath.Join(r.W.StateDir(), "git_known_hosts"))
	args := []string{"ssh", "-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=accept-new", "-o", "UserKnownHostsFile=" + shq(known),
		"-o", "ConnectTimeout=30", "-o", "ServerAliveInterval=15"}
	if ref := r.R.Str("deployKey"); ref != "" {
		it, _, err := r.m.Hub.Credential(ctx, r.W, ref)
		if err != nil {
			return "", err
		}
		key, err := parseKey(it)
		if err != nil {
			return "", fmt.Errorf("deploy key %s: %w", ref, err)
		}
		if runtime.GOOS == "windows" {
			// Windows' OpenSSH talks to agents over named pipes: use a
			// private key file that lives as long as the git process.
			pem, err := ssh.MarshalPrivateKey(key, "")
			if err != nil {
				return "", err
			}
			p := filepath.Join(tmp, "id")
			if err := os.WriteFile(p, pem.Bytes, 0o600); err != nil {
				return "", err
			}
			args = append(args, "-o", "IdentitiesOnly=yes", "-o", "IdentityAgent=none", "-i", shq(filepath.ToSlash(p)))
		} else {
			sock, pub, err := serveAgent(e, tmp, key)
			if err != nil {
				return "", err
			}
			args = append(args, "-o", "IdentitiesOnly=yes", "-o", "IdentityAgent="+shq(sock), "-o", "IdentityFile="+shq(pub))
		}
	}
	if proxy != "" {
		exe := r.m.Executable
		if exe == "" {
			exe, _ = os.Executable()
		}
		args = append(args, "-o", shq("ProxyCommand="+filepath.ToSlash(exe)+" socks-connect "+proxy+" %h %p"))
	}
	return strings.Join(args, " "), nil
}

func parseKey(it vault.Item) (any, error) {
	pk := it.Get("private_key")
	if pk == "" {
		return nil, errors.New("the item has no private key")
	}
	if pass := it.Get("passphrase"); pass != "" {
		return ssh.ParseRawPrivateKeyWithPassphrase([]byte(pk), []byte(pass))
	}
	return ssh.ParseRawPrivateKey([]byte(pk))
}

// serveAgent runs an ssh-agent with only key on a socket in tmp (0700).
// Returns the socket and a public key file selecting the key.
func serveAgent(e *env, tmp string, key any) (sock, pubFile string, err error) {
	kr := agent.NewKeyring()
	if err := kr.Add(agent.AddedKey{PrivateKey: key, Comment: "opsdeck deploy key"}); err != nil {
		return "", "", err
	}
	signer, err := ssh.NewSignerFromKey(key)
	if err != nil {
		return "", "", err
	}
	pubFile = filepath.Join(tmp, "id.pub")
	if err := os.WriteFile(pubFile, ssh.MarshalAuthorizedKey(signer.PublicKey()), 0o600); err != nil {
		return "", "", err
	}
	sock = filepath.Join(tmp, "agent.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		return "", "", err
	}
	os.Chmod(sock, 0o600)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			wg.Add(1)
			go func() { defer wg.Done(); defer c.Close(); agent.ServeAgent(kr, c) }()
		}
	}()
	e.closers = append(e.closers, func() { ln.Close(); kr.RemoveAll() })
	return sock, pubFile, nil
}

// Error is a failed git command.
type Error struct {
	Args   []string
	Stderr string
	Code   int
}

func (e *Error) Error() string {
	msg := strings.TrimSpace(e.Stderr)
	if msg == "" {
		msg = fmt.Sprintf("exit status %d", e.Code)
	}
	cmd := "git"
	if len(e.Args) > 0 {
		cmd += " " + e.Args[0]
	}
	return cmd + ": " + msg + hint(msg)
}

func hint(stderr string) string {
	s := strings.ToLower(stderr)
	switch {
	case strings.Contains(s, "authentication failed"), strings.Contains(s, "could not read username"), strings.Contains(s, "http basic: access denied"),
		strings.Contains(s, "invalid username or password"), strings.Contains(s, "terminal prompts disabled"):
		return "\n(hint: set an access token with read/write repository access on the repository resource)"
	case strings.Contains(s, "permission denied (publickey"):
		return "\n(hint: add the deploy key's public key to the repository with write access, or use an HTTPS URL with a token)"
	case strings.Contains(s, "host key verification failed"), strings.Contains(s, "remote host identification has changed"):
		return "\n(hint: the server's SSH host key changed since it was first seen; if that is expected, remove its line from .opsdeck/git_known_hosts in the workspace)"
	case strings.Contains(s, "certificate"):
		return "\n(hint: for a private CA, paste its certificate into the repository's CA certificate field)"
	case strings.Contains(s, "non-fast-forward"), strings.Contains(s, "fetch first"):
		return "\n(hint: the remote branch has new commits: pull first, then push)"
	case strings.Contains(s, "protected branch"), strings.Contains(s, "gh006"):
		return "\n(hint: the branch is protected on the server: push to a new branch and open a merge request)"
	}
	return ""
}

// capped collects up to max bytes.
type capped struct {
	buf       bytes.Buffer
	max       int
	truncated bool
}

func (c *capped) Write(p []byte) (int, error) {
	if room := c.max - c.buf.Len(); room > 0 {
		if len(p) > room {
			c.buf.Write(p[:room])
			c.truncated = true
		} else {
			c.buf.Write(p)
		}
	} else if len(p) > 0 {
		c.truncated = true
	}
	return len(p), nil
}

// run executes git in dir. Output above max bytes is dropped (truncated).
func run(ctx context.Context, e *env, dir string, stdin []byte, max int, args ...string) (out string, truncated bool, err error) {
	bin, err := exec.LookPath("git")
	if err != nil {
		return "", false, errors.New("git is not installed on this machine (install Git and restart opsdeck)")
	}
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 10*time.Minute)
		defer cancel()
	}
	cmd := sysproc.CommandContext(ctx, bin, args...)
	cmd.Dir = dir
	cmd.Env = e.list()
	cmd.WaitDelay = 5 * time.Second
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	if max <= 0 {
		max = 4 << 20
	}
	stdout := &capped{max: max}
	stderr := &capped{max: 64 << 10}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	err = cmd.Run()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return stdout.buf.String(), stdout.truncated, &Error{Args: args, Stderr: stderr.buf.String(), Code: ee.ExitCode()}
		}
		if ctx.Err() != nil {
			return stdout.buf.String(), stdout.truncated, fmt.Errorf("git %s: %w", args[0], ctx.Err())
		}
		return "", false, err
	}
	return stdout.buf.String(), stdout.truncated, nil
}

// exitCode is the exit status of a failed git command (-1 otherwise).
func exitCode(err error) int {
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	return -1
}

// ----------------------------------------------------------------------------
// Hosting API

// httpClient reaches the hosting API the way git reaches the server.
func (r *Repo) httpClient(ctx context.Context) (*http.Client, error) {
	tr := &http.Transport{TLSHandshakeTimeout: 20 * time.Second, ForceAttemptHTTP2: true, IdleConnTimeout: 30 * time.Second}
	if r.R.Via != "" {
		dial, err := r.m.Hub.Dialer(ctx, r.W, r.R)
		if err != nil {
			return nil, err
		}
		tr.DialContext = dial
	} else {
		tr.Proxy = http.ProxyFromEnvironment
	}
	if ca := r.caCert(); ca != "" {
		pool, err := x509.SystemCertPool()
		if err != nil || pool == nil {
			pool = x509.NewCertPool()
		}
		if !pool.AppendCertsFromPEM([]byte(ca)) {
			return nil, errors.New("the repository's CA certificate is not valid PEM")
		}
		tr.TLSClientConfig = &tls.Config{RootCAs: pool}
	}
	return &http.Client{Transport: tr, Timeout: 60 * time.Second}, nil
}

// Provider returns the hosting API client of the repository.
func (r *Repo) Provider(ctx context.Context) (Provider, error) {
	if r.Remote.Scheme == "file" {
		return nil, errors.New("local repositories have no hosting API")
	}
	c, err := r.httpClient(ctx)
	if err != nil {
		return nil, err
	}
	_, token, err := r.token(ctx)
	if err != nil {
		return nil, err
	}
	setting := r.R.Str("provider")
	if setting == "auto" {
		setting = ""
	}
	kind, base := setting, strings.TrimSuffix(r.R.Str("apiUrl"), "/")
	if c := r.Conn; c != nil && (kind == "" || kind == c.Provider) {
		kind = c.Provider
		if base == "" {
			base = APIBase(*c)
		}
	}
	if base == "" || kind == "" {
		ck := r.Remote.Origin() + "|" + setting
		r.m.mu.Lock()
		cached, ok := r.m.kinds[ck]
		r.m.mu.Unlock()
		if !ok {
			k, b, err := detect(ctx, c, r.Remote, setting)
			if err != nil {
				return nil, err
			}
			cached = [2]string{k, b}
			r.m.mu.Lock()
			r.m.kinds[ck] = cached
			r.m.mu.Unlock()
		}
		if kind == "" {
			kind = cached[0]
		}
		if base == "" {
			base = cached[1]
		}
	}
	switch kind {
	case "github":
		return newGitHub(c, base, r.Remote.Path, token)
	case "gitlab":
		return newGitLab(c, base, r.Remote.Path, token), nil
	}
	return nil, fmt.Errorf("unknown provider %q", kind)
}
