package ansible

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
	sshagent "golang.org/x/crypto/ssh/agent"
	"gopkg.in/yaml.v3"

	"opsdeck/internal/vpn"
	"opsdeck/internal/workspace"
)

// procEnv is the environment of one ansible process and the helpers it
// needs (password files, generated inventory, ssh-agent, proxy). Everything
// lives in a private temporary folder removed by close.
type procEnv struct {
	dir       string
	env       []string
	inventory string
	extraVars string
	vaultArgs []string
	shown     map[string]string // real path -> how it is shown in the command
	notes     []string          // lines shown at the start of the log
	hosts     []string          // workspace hosts of a generated inventory
	closers   []func()
}

func (pe *procEnv) close() {
	for i := len(pe.closers) - 1; i >= 0; i-- {
		pe.closers[i]()
	}
	pe.closers = nil
}

// prepare builds the environment of a run.
func (m *Manager) prepare(ctx context.Context, p *Project, o RunOptions, withVault bool) (pe *procEnv, err error) {
	pe = &procEnv{shown: map[string]string{}}
	defer func() {
		if err != nil {
			pe.close()
		}
	}()
	dir, err := os.MkdirTemp("", "opsdeck-ansible-")
	if err != nil {
		return nil, err
	}
	os.Chmod(dir, 0o700)
	pe.dir = dir
	pe.closers = append(pe.closers, func() { os.RemoveAll(dir) })
	pe.env = baseEnv(p)
	pe.env = append(pe.env, "ANSIBLE_LOCAL_TEMP="+filepath.Join(dir, "tmp"))

	// Vault passwords: one file per vault id, readable only by this user,
	// removed with the folder.
	if withVault {
		pw := m.Passwords(ctx, p)
		ids := make([]string, 0, len(pw))
		for id := range pw {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		for i, id := range ids {
			f := filepath.Join(dir, "vault-"+strconv.Itoa(i))
			if err := os.WriteFile(f, []byte(pw[id]+"\n"), 0o600); err != nil {
				return nil, err
			}
			pe.vaultArgs = append(pe.vaultArgs, "--vault-id="+id+"@"+f)
			pe.shown[f] = "<password>"
		}
	}

	if strings.TrimSpace(o.ExtraVars) != "" {
		f := filepath.Join(dir, "extra-vars.yml")
		if err := os.WriteFile(f, []byte(o.ExtraVars), 0o600); err != nil {
			return nil, err
		}
		pe.extraVars = f
		pe.shown[f] = "extra-vars.yml"
	}

	// Keys for ssh: the project's own key and, for the workspace inventory,
	// the keys of its hosts — served by an ssh-agent that lives in memory.
	var keys []any
	if ref := p.R.Str("sshKey"); ref != "" && m.Hub != nil {
		it, _, err := m.Hub.Credential(ctx, p.W, ref)
		if err != nil {
			return nil, fmt.Errorf("ssh key %s: %w", ref, err)
		}
		k, err := parseKey(it.Get("private_key"), it.Get("passphrase"))
		if err != nil {
			return nil, fmt.Errorf("ssh key %s: %w", ref, err)
		}
		keys = append(keys, k)
	}

	inv := o.Inventory
	if inv == "" {
		inv = p.DefaultInventory()
	}
	var hosts []*workspace.Resource
	if inv == WorkspaceInventory {
		hosts = workspaceHosts(p.W)
		if len(hosts) == 0 {
			return nil, errors.New("the workspace has no hosts for the workspace inventory")
		}
		for _, h := range hosts {
			pe.hosts = append(pe.hosts, h.Name)
			ref := firstNonEmpty(h.Str("auth"), p.W.Settings.DefaultAuth)
			if ref == "" || m.Hub == nil {
				continue
			}
			it, _, err := m.Hub.Credential(ctx, p.W, ref)
			if err != nil {
				pe.notes = append(pe.notes, fmt.Sprintf("opsdeck: %s: %v", h.Name, err))
				continue
			}
			if it.Type != "ssh-key" {
				pe.notes = append(pe.notes, fmt.Sprintf("opsdeck: %s logs in with a password (%s); ansible needs an SSH key here — add one to the host", h.Name, ref))
				continue
			}
			k, err := parseKey(it.Get("private_key"), it.Get("passphrase"))
			if err != nil {
				pe.notes = append(pe.notes, fmt.Sprintf("opsdeck: %s: key %s: %v", h.Name, ref, err))
				continue
			}
			keys = append(keys, k)
		}
	}
	if len(keys) > 0 {
		sock, err := serveAgent(pe, keys)
		if err != nil {
			return nil, err
		}
		pe.env = append(pe.env, "SSH_AUTH_SOCK="+sock)
	}

	// Reaching hosts through a VPN or a jump host: a SOCKS proxy on
	// 127.0.0.1 with a random password, used by ssh as ProxyCommand.
	needProxy := p.R.Via != ""
	for _, h := range hosts {
		if h.Via != "" {
			needProxy = true
		}
	}
	sshArgs := ""
	if needProxy || len(hosts) > 0 {
		cp := filepath.Join(dir, "cp")
		os.Mkdir(cp, 0o700)
		pe.env = append(pe.env, "ANSIBLE_SSH_CONTROL_PATH_DIR="+cp)
		known := []string{filepath.Join(p.W.StateDir(), "ansible_known_hosts"), filepath.Join(p.W.StateDir(), "known_hosts")}
		os.MkdirAll(p.W.StateDir(), 0o700)
		sshArgs = `-o StrictHostKeyChecking=accept-new -o "UserKnownHostsFile=` + filepath.ToSlash(known[0]) + " " + filepath.ToSlash(known[1]) + `" -o ServerAliveInterval=15`
	}
	if needProxy {
		user, pass := "ansible", randToken()
		srv, err := vpn.ServeSOCKSAuth(0, m.proxyDial(ctx, p), user, pass)
		if err != nil {
			return nil, err
		}
		pe.closers = append(pe.closers, func() { srv.Close() })
		pe.env = append(pe.env, "OPSDECK_SOCKS_AUTH="+user+":"+pass)
		exe := m.Executable
		if exe == "" {
			exe, _ = os.Executable()
		}
		cmd := shq(filepath.ToSlash(exe)) + " socks-connect " + srv.Addr() + " %h %p"
		sshArgs += ` -o "ProxyCommand=` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(cmd) + `"`
		if p.R.Via != "" {
			pe.notes = append(pe.notes, "opsdeck: hosts are reached through "+p.R.Via)
		}
	}

	switch {
	case inv == WorkspaceInventory:
		y, err := WorkspaceInventoryYAML(p.W, hosts, sshArgs)
		if err != nil {
			return nil, err
		}
		f := filepath.Join(dir, "workspace-inventory.yml")
		if err := os.WriteFile(f, y, 0o600); err != nil {
			return nil, err
		}
		pe.inventory = f
		pe.shown[f] = "<workspace hosts>"
		names := pe.hosts
		if len(names) > 12 {
			names = append(append([]string{}, names[:12]...), fmt.Sprintf("… %d more", len(pe.hosts)-12))
		}
		pe.notes = append(pe.notes, "opsdeck: workspace inventory: "+strings.Join(names, ", "))
	case inv != "":
		pe.inventory = inv
		if sshArgs != "" {
			pe.env = append(pe.env, "ANSIBLE_SSH_COMMON_ARGS="+sshArgs)
		}
	default:
		if sshArgs != "" {
			pe.env = append(pe.env, "ANSIBLE_SSH_COMMON_ARGS="+sshArgs)
		}
	}
	return pe, nil
}

func randToken() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return newID()
	}
	return fmt.Sprintf("%x", b)
}

func shq(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// proxyDial reaches an address the way opsdeck reaches it: a workspace host
// (by address or name) through its own via chain, anything else through
// the project's via, else directly.
func (m *Manager) proxyDial(ctx context.Context, p *Project) vpn.DialFunc {
	return func(dctx context.Context, network, addr string) (net.Conn, error) {
		host, _, _ := net.SplitHostPort(addr)
		target := p.R
		for _, h := range workspaceHosts(p.W) {
			if h.Str("address") == host || h.Name == host {
				target = h
				break
			}
		}
		if m.Hub == nil {
			var d net.Dialer
			return d.DialContext(dctx, network, addr)
		}
		dial, err := m.Hub.Dialer(ctx, p.W, target)
		if err != nil {
			return nil, err
		}
		return dial(dctx, network, addr)
	}
}

func workspaceHosts(w *workspace.Workspace) []*workspace.Resource {
	var out []*workspace.Resource
	for _, r := range w.Resources {
		if r.Kind == "Host" {
			out = append(out, r)
		}
	}
	return out
}

var groupRe = regexp.MustCompile(`[^A-Za-z0-9_]`)

func groupName(s string) string {
	g := groupRe.ReplaceAllString(s, "_")
	if g != "" && g[0] >= '0' && g[0] <= '9' {
		g = "_" + g
	}
	return g
}

// WorkspaceInventoryYAML is an inventory of the workspace's hosts: groups
// for host groups, roles and environments; address, port and user of each
// host; sshArgs (proxy, known hosts) on every host when given.
func WorkspaceInventoryYAML(w *workspace.Workspace, hosts []*workspace.Resource, sshArgs string) ([]byte, error) {
	type hostVars map[string]any
	all := map[string]any{}
	hv := map[string]hostVars{}
	for _, h := range hosts {
		v := hostVars{}
		addr := h.Str("address")
		if a, port, err := net.SplitHostPort(addr); err == nil {
			addr = a
			if n, err := strconv.Atoi(port); err == nil {
				v["ansible_port"] = n
			}
		}
		if addr != "" {
			v["ansible_host"] = addr
		}
		if port := h.Int("port", 0); port > 0 {
			v["ansible_port"] = port
		}
		if u := firstNonEmpty(h.Str("user"), w.Settings.DefaultUser); u != "" {
			v["ansible_user"] = u
		}
		if sshArgs != "" {
			v["ansible_ssh_common_args"] = sshArgs
		}
		if h.Environment != "" {
			v["opsdeck_environment"] = h.Environment
		}
		hv[h.Name] = v
	}
	all["hosts"] = hv
	children := map[string]map[string]any{}
	add := func(group, host string) {
		g := groupName(group)
		if g == "" || g == "all" || g == "ungrouped" {
			return
		}
		if _, isHost := hv[g]; isHost {
			g = "group_" + g
		}
		if children[g] == nil {
			children[g] = map[string]any{"hosts": map[string]any{}}
		}
		children[g]["hosts"].(map[string]any)[host] = map[string]any{}
	}
	inSet := map[string]bool{}
	for _, h := range hosts {
		inSet[h.Name] = true
		if h.Environment != "" {
			add(h.Environment, h.Name)
		}
		for _, role := range h.List("roles") {
			add(role, h.Name)
		}
	}
	for _, r := range w.Resources {
		if r.Kind != "HostGroup" {
			continue
		}
		for _, h := range w.GroupMembers(r) {
			if inSet[h.Name] {
				add(r.Name, h.Name)
			}
		}
	}
	if len(children) > 0 {
		c := map[string]any{}
		for k, v := range children {
			c[k] = v
		}
		all["children"] = c
	}
	b, err := yaml.Marshal(map[string]any{"all": all})
	if err != nil {
		return nil, err
	}
	return append([]byte("# generated by opsdeck from the workspace's hosts\n"), b...), nil
}

// ---------------------------------------------------------------------------
// ssh-agent in memory

func parseKey(pem, passphrase string) (any, error) {
	if pem == "" {
		return nil, errors.New("the item has no private key")
	}
	if passphrase != "" {
		return ssh.ParseRawPrivateKeyWithPassphrase([]byte(pem), []byte(passphrase))
	}
	return ssh.ParseRawPrivateKey([]byte(pem))
}

// serveAgent runs an ssh-agent holding keys on a socket in pe's folder.
func serveAgent(pe *procEnv, keys []any) (string, error) {
	kr := sshagent.NewKeyring()
	for _, k := range keys {
		if err := kr.Add(sshagent.AddedKey{PrivateKey: k, Comment: "opsdeck"}); err != nil {
			return "", err
		}
	}
	sock := filepath.Join(pe.dir, "agent.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		return "", err
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
			go func() { defer c.Close(); sshagent.ServeAgent(kr, c) }()
		}
	}()
	pe.closers = append(pe.closers, func() { ln.Close(); wg.Wait(); kr.RemoveAll() })
	return sock, nil
}

// ---------------------------------------------------------------------------
// Short commands (inventory, syntax check, list hosts/tasks/tags)

// Exec runs a short ansible command in the project and returns its output
// (without colours). withVault passes the vault passwords at hand.
func (m *Manager) Exec(ctx context.Context, p *Project, o RunOptions, withVault bool, prog string, args ...string) (string, error) {
	if !p.Exists() {
		return "", fmt.Errorf("the project folder %s does not exist", p.Root)
	}
	path, err := lookPath(p, prog)
	if err != nil {
		return "", err
	}
	pe, err := m.prepare(ctx, p, o, withVault)
	if err != nil {
		return "", err
	}
	defer pe.close()
	full := append([]string{}, args...)
	if pe.inventory != "" && prog != "ansible-galaxy" {
		full = append([]string{"--inventory=" + pe.inventory}, full...)
	}
	if prog != "ansible-galaxy" && prog != "ansible-lint" {
		full = append(append([]string{}, pe.vaultArgs...), full...)
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, full...)
	cmd.Dir = p.Root
	cmd.Env = append(pe.env, "ANSIBLE_FORCE_COLOR=0", "PY_COLORS=0", "NO_COLOR=1", "ANSIBLE_NOCOLOR=1")
	setProcessGroup(cmd)
	cmd.Cancel = func() error { kill(cmd); return nil }
	out, err := cmd.CombinedOutput()
	text := StripANSI(string(out))
	if m.Redact != nil {
		text = m.Redact(text)
	}
	if err != nil {
		if ctx.Err() != nil {
			return text, errors.New("timed out after 2 minutes")
		}
		return text, fmt.Errorf("%s failed: %s", prog, lastLines(text, 6))
	}
	return text, nil
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
