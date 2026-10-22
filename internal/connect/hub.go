// Package connect reaches workspace resources: it brings up networks (VPN
// profiles from internal/vpn, SOCKS proxies), chains SSH jump hosts, and
// pools SSH connections.
// Credentials come from the vault and are used, never returned.
package connect

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"

	"opsdeck/internal/vault"
	"opsdeck/internal/vpn"
	"opsdeck/internal/workspace"
)

// Actor identifies who is acting; used for credential policy.
type Actor struct {
	Kind    string // "user" or "agent"
	Session string
}

type actorKey struct{}

func WithActor(ctx context.Context, a Actor) context.Context {
	return context.WithValue(ctx, actorKey{}, a)
}

func ActorFrom(ctx context.Context) Actor {
	if a, ok := ctx.Value(actorKey{}).(Actor); ok {
		return a
	}
	return Actor{Kind: "user"}
}

// DialFunc dials an address from some point of the network.
type DialFunc func(ctx context.Context, network, addr string) (net.Conn, error)

// Hub owns live networks and SSH connections. Safe for concurrent use.
type Hub struct {
	vault    *vault.Vault
	stateDir func(ws string) string

	// Elevate returns the command prefix for system-mode VPN clients.
	Elevate func() []string
	// Browser returns the configured browser for VPN single sign-on.
	Browser func() string

	mu       sync.Mutex
	networks map[string]*network // key ws/name
	netLogs  map[string]*vpn.Log // key ws/name, kept after a network goes down
	clients  map[string]*sshConn // key ws/host
	dialing  map[string]*sync.Mutex
}

type sshConn struct {
	client *ssh.Client
	done   chan struct{}
}

func NewHub(v *vault.Vault, stateDir func(ws string) string) *Hub {
	return &Hub{vault: v, stateDir: stateDir, networks: map[string]*network{}, netLogs: map[string]*vpn.Log{},
		clients: map[string]*sshConn{}, dialing: map[string]*sync.Mutex{}}
}

// credential resolves a vault reference for use by a workspace, enforcing
// scope isolation and the agent policy.
func (h *Hub) credential(ctx context.Context, w *workspace.Workspace, ref string) (vault.Item, error) {
	it, _, err := h.vault.ResolveFor(w.Name(), ref)
	if err != nil {
		return vault.Item{}, err
	}
	h.vault.Touch()
	if a := ActorFrom(ctx); a.Kind == "agent" && !it.Policy.AgentUse {
		return vault.Item{}, fmt.Errorf("credential %s is not allowed for agent use (enable it in the vault item's policy)", ref)
	}
	return it, nil
}

// Credentials lists the vault references needed to reach and log into r
// (used by the agent permission layer for confirm-on-use).
func (h *Hub) Credentials(w *workspace.Workspace, r *workspace.Resource) []string {
	var refs []string
	chain, _ := w.Chain(r)
	for _, hop := range append(chain, r) {
		switch {
		case hop.Kind == "Host":
			if ref := authRef(w, hop); ref != "" {
				refs = append(refs, ref)
			}
		case workspace.IsNetworkKind(hop.Kind):
			for _, f := range secretFields(hop) {
				refs = append(refs, hop.Str(f))
			}
		}
	}
	return refs
}

// authorize checks every credential on the way to r (networks, jump hosts,
// the target itself). It runs on every use, including pooled connections and
// networks that are already up.
func (h *Hub) authorize(ctx context.Context, w *workspace.Workspace, r *workspace.Resource) error {
	for _, ref := range h.Credentials(w, r) {
		if _, err := h.credential(ctx, w, ref); err != nil {
			return err
		}
	}
	return nil
}

func authRef(w *workspace.Workspace, host *workspace.Resource) string {
	if ref := host.Str("auth"); ref != "" {
		return ref
	}
	return w.Settings.DefaultAuth
}

// ----------------------------------------------------------------------------
// Dialing through via chains

// Dialer returns a dial function that reaches addresses the way r is reached
// (through r.Via), plus a human readable description of the path.
func (h *Hub) Dialer(ctx context.Context, w *workspace.Workspace, r *workspace.Resource) (DialFunc, error) {
	if r.Via != "" {
		if hop := w.FindRef(r.Via); hop != nil {
			if err := h.authorize(ctx, w, hop); err != nil {
				return nil, err
			}
		}
	}
	if r.Via == "" {
		var d net.Dialer
		return d.DialContext, nil
	}
	hop := w.FindRef(r.Via)
	if hop == nil {
		return nil, fmt.Errorf("%s: via %q not found", r.ID(), r.Via)
	}
	if _, err := w.Chain(r); err != nil {
		return nil, err
	}
	switch {
	case hop.Kind == "Host":
		client, err := h.SSH(ctx, w, hop)
		if err != nil {
			return nil, fmt.Errorf("jump host %s: %w", hop.Name, err)
		}
		return client.DialContext, nil
	case workspace.IsNetworkKind(hop.Kind):
		n, err := h.networkUp(ctx, w, hop)
		if err != nil {
			return nil, err
		}
		return n.dial, nil
	}
	return nil, fmt.Errorf("via must be a host or a network, not %s", hop.Kind)
}

// DescribePath returns e.g. ["network/office-wg", "host/bastion"].
func DescribePath(w *workspace.Workspace, r *workspace.Resource) ([]string, error) {
	chain, err := w.Chain(r)
	if err != nil {
		return nil, err
	}
	out := []string{}
	for _, hop := range chain {
		out = append(out, hop.ID())
	}
	return out, nil
}

// ----------------------------------------------------------------------------
// SSH

func (h *Hub) keyLock(key string) *sync.Mutex {
	h.mu.Lock()
	defer h.mu.Unlock()
	l := h.dialing[key]
	if l == nil {
		l = &sync.Mutex{}
		h.dialing[key] = l
	}
	return l
}

// SSH returns a pooled SSH client for a Host resource, connecting through its
// via chain when needed.
func (h *Hub) SSH(ctx context.Context, w *workspace.Workspace, host *workspace.Resource) (*ssh.Client, error) {
	if host.Kind != "Host" {
		return nil, fmt.Errorf("%s is not a host", host.ID())
	}
	key := w.Name() + "/" + host.Name
	lock := h.keyLock("ssh:" + key)
	lock.Lock()
	defer lock.Unlock()

	h.mu.Lock()
	if c := h.clients[key]; c != nil {
		select {
		case <-c.done:
			delete(h.clients, key)
		default:
			h.mu.Unlock()
			// The whole chain is policy-checked on every use.
			if err := h.authorize(ctx, w, host); err != nil {
				return nil, err
			}
			return c.client, nil
		}
	}
	h.mu.Unlock()

	client, err := h.dialSSH(ctx, w, host)
	if err != nil {
		return nil, err
	}
	c := &sshConn{client: client, done: make(chan struct{})}
	go func() {
		client.Wait()
		close(c.done)
	}()
	go keepalive(client, c.done)
	h.mu.Lock()
	h.clients[key] = c
	h.mu.Unlock()
	return client, nil
}

func keepalive(c *ssh.Client, done chan struct{}) {
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-done:
			return
		case <-t.C:
			errc := make(chan error, 1)
			go func() { _, _, err := c.SendRequest("keepalive@openssh.com", true, nil); errc <- err }()
			select {
			case err := <-errc:
				if err != nil {
					c.Close()
					return
				}
			case <-time.After(15 * time.Second):
				c.Close()
				return
			}
		}
	}
}

func hostAddr(host *workspace.Resource) string {
	return net.JoinHostPort(host.Str("address"), strconv.Itoa(host.Int("port", 22)))
}

func (h *Hub) dialSSH(ctx context.Context, w *workspace.Workspace, host *workspace.Resource) (*ssh.Client, error) {
	if host.Str("address") == "" {
		return nil, fmt.Errorf("%s has no address", host.ID())
	}
	cfg, err := h.clientConfig(ctx, w, host)
	if err != nil {
		return nil, err
	}
	dial, err := h.Dialer(ctx, w, host)
	if err != nil {
		return nil, err
	}
	addr := hostAddr(host)
	dctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	conn, err := dial(dctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("connect %s (%s): %w", host.Name, addr, err)
	}
	// Bound the handshake too.
	conn.SetDeadline(time.Now().Add(20 * time.Second))
	cc, chans, reqs, err := ssh.NewClientConn(conn, addr, cfg)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("ssh %s: %w", host.Name, err)
	}
	conn.SetDeadline(time.Time{})
	return ssh.NewClient(cc, chans, reqs), nil
}

func (h *Hub) clientConfig(ctx context.Context, w *workspace.Workspace, host *workspace.Resource) (*ssh.ClientConfig, error) {
	ref := authRef(w, host)
	if ref == "" {
		return nil, fmt.Errorf("%s: no credential configured (set spec.auth or the workspace default)", host.ID())
	}
	it, err := h.credential(ctx, w, ref)
	if err != nil {
		return nil, err
	}
	user := host.Str("user")
	if user == "" {
		user = it.Get("username")
	}
	if user == "" {
		user = w.Settings.DefaultUser
	}
	if user == "" {
		user = "root"
	}
	var auth []ssh.AuthMethod
	switch it.Type {
	case "ssh-key":
		signer, err := vault.Signer(it)
		if err != nil {
			return nil, err
		}
		auth = append(auth, ssh.PublicKeys(signer))
	case "login":
		pw := it.Get("password")
		auth = append(auth, ssh.Password(pw), ssh.KeyboardInteractive(func(_, _ string, qs []string, _ []bool) ([]string, error) {
			ans := make([]string, len(qs))
			for i := range qs {
				ans[i] = pw
			}
			return ans, nil
		}))
	default:
		return nil, fmt.Errorf("%s: credential %s is a %s; use an ssh-key or login", host.ID(), ref, it.Type)
	}
	return &ssh.ClientConfig{
		User:              user,
		Auth:              auth,
		HostKeyCallback:   h.hostKeyCallback(w.Name()),
		HostKeyAlgorithms: h.hostKeyAlgorithms(w.Name(), hostAddr(host)),
		Timeout:           20 * time.Second,
	}, nil
}

// ----------------------------------------------------------------------------
// Known hosts: trust on first use, per workspace; a changed key is an error.

var ErrHostKeyChanged = errors.New("host key changed")

func (h *Hub) knownHostsFile(ws string) string {
	return filepath.Join(h.stateDir(ws), "known_hosts")
}

func (h *Hub) knownKeys(ws, host string) []ssh.PublicKey {
	lines, _ := os.ReadFile(h.knownHostsFile(ws))
	var out []ssh.PublicKey
	for _, line := range strings.Split(string(lines), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 || fields[0] != host {
			continue
		}
		if k, _, _, _, err := ssh.ParseAuthorizedKey([]byte(strings.Join(fields[1:], " "))); err == nil {
			out = append(out, k)
		}
	}
	return out
}

// hostKeyAlgorithms restricts negotiation to the types already pinned for a
// host, so a server (or attacker) cannot switch to an unpinned key type.
func (h *Hub) hostKeyAlgorithms(ws, addr string) []string {
	h.mu.Lock()
	keys := h.knownKeys(ws, normalizeHost(addr))
	h.mu.Unlock()
	var algos []string
	for _, k := range keys {
		switch k.Type() {
		case ssh.KeyAlgoRSA:
			algos = append(algos, ssh.KeyAlgoRSASHA512, ssh.KeyAlgoRSASHA256, ssh.KeyAlgoRSA)
		default:
			algos = append(algos, k.Type())
		}
	}
	return algos
}

func (h *Hub) hostKeyCallback(ws string) ssh.HostKeyCallback {
	return func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		path := h.knownHostsFile(ws)
		h.mu.Lock()
		defer h.mu.Unlock()
		host := normalizeHost(hostname)
		known := h.knownKeys(ws, host)
		for _, k := range known {
			if string(k.Marshal()) == string(key.Marshal()) {
				return nil
			}
		}
		if len(known) > 0 {
			return fmt.Errorf("%w for %s: expected %s, got %s %s — possible MITM. If the server was reinstalled, forget the old key in the host's menu",
				ErrHostKeyChanged, host, ssh.FingerprintSHA256(known[0]), key.Type(), ssh.FingerprintSHA256(key))
		}
		os.MkdirAll(filepath.Dir(path), 0o700)
		f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = fmt.Fprintf(f, "%s %s", host, ssh.MarshalAuthorizedKey(key))
		return err
	}
}

func normalizeHost(hostport string) string {
	host, port, err := net.SplitHostPort(hostport)
	if err != nil {
		return hostport
	}
	if port == "22" {
		return host
	}
	return "[" + host + "]:" + port
}

// ForgetHostKey removes the pinned key(s) for address (host or host:port).
func (h *Hub) ForgetHostKey(ws, address string) error {
	path := h.knownHostsFile(ws)
	h.mu.Lock()
	defer h.mu.Unlock()
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	target := address
	if _, _, err := net.SplitHostPort(address); err == nil {
		target = normalizeHost(address)
	}
	var keep []string
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) > 0 && f[0] != target {
			keep = append(keep, line)
		}
	}
	return os.WriteFile(path, []byte(strings.Join(keep, "\n")+"\n"), 0o600)
}

// dropBroken forgets clients whose connection is gone.
func (h *Hub) dropBroken(ws string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for k, c := range h.clients {
		if strings.HasPrefix(k, ws+"/") {
			select {
			case <-c.done:
				delete(h.clients, k)
			default:
			}
		}
	}
}

// Disconnect closes the pooled connection of a host.
func (h *Hub) Disconnect(ws, host string) {
	h.mu.Lock()
	c := h.clients[ws+"/"+host]
	delete(h.clients, ws+"/"+host)
	h.mu.Unlock()
	if c != nil {
		c.client.Close()
	}
}

// CloseWorkspace drops every connection and network of a workspace.
func (h *Hub) CloseWorkspace(ws string) {
	h.mu.Lock()
	var clients []*sshConn
	var nets []*network
	for k, c := range h.clients {
		if strings.HasPrefix(k, ws+"/") {
			clients = append(clients, c)
			delete(h.clients, k)
		}
	}
	for k, n := range h.networks {
		if strings.HasPrefix(k, ws+"/") {
			nets = append(nets, n)
			delete(h.networks, k)
		}
	}
	h.mu.Unlock()
	for _, c := range clients {
		c.client.Close()
	}
	var wg sync.WaitGroup
	for _, n := range nets {
		wg.Add(1)
		go func() { defer wg.Done(); n.stop() }()
	}
	wg.Wait()
}

// CloseAll shuts everything down.
func (h *Hub) CloseAll() {
	h.mu.Lock()
	var wss []string
	seen := map[string]bool{}
	for k := range h.clients {
		ws, _, _ := strings.Cut(k, "/")
		if !seen[ws] {
			seen[ws] = true
			wss = append(wss, ws)
		}
	}
	for k := range h.networks {
		ws, _, _ := strings.Cut(k, "/")
		if !seen[ws] {
			seen[ws] = true
			wss = append(wss, ws)
		}
	}
	h.mu.Unlock()
	for _, ws := range wss {
		h.CloseWorkspace(ws)
	}
}
