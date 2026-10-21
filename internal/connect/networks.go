package connect

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/net/proxy"

	"opsdeck/internal/vault"
	"opsdeck/internal/vpn"
	"opsdeck/internal/workspace"
)

// network is a VPN profile or SOCKS proxy that was brought up (or tried).
type network struct {
	kind, mode string
	state      string // connecting | up | error
	err        string
	authURL    string // waiting for the user to log in
	since      time.Time
	tun        vpn.Tunnel
	dial       DialFunc
	socks      *vpn.SocksServer
	proxy      string // local SOCKS5 address other apps can use
	ready      chan struct{}
	cancel     context.CancelFunc
}

func (n *network) stop() {
	if n.cancel != nil {
		n.cancel()
	}
	if n.socks != nil {
		n.socks.Close()
	}
	if n.tun != nil {
		n.tun.Close()
	}
}

func netKey(ws, name string) string { return ws + "/" + name }

// secretFields lists the vault reference fields set on a resource.
func secretFields(r *workspace.Resource) []string {
	ks, ok := workspace.KindByName(r.Kind)
	if !ok {
		return nil
	}
	var out []string
	for _, f := range ks.Fields {
		if f.Type == "secretRef" && r.Str(f.Key) != "" {
			out = append(out, f.Key)
		}
	}
	return out
}

func (h *Hub) elevate() []string {
	if h.Elevate == nil {
		return vpn.Elevation("auto")
	}
	return h.Elevate()
}

func (h *Hub) netLog(key string) *vpn.Log {
	l := h.netLogs[key]
	if l == nil {
		l = vpn.NewLog()
		h.netLogs[key] = l
	}
	return l
}

// connectTimeout bounds one connection attempt; an interactive login
// (Tailscale without an auth key) gets time for the user to finish it.
func connectTimeout(r *workspace.Resource) time.Duration {
	if r.Kind == "Tailscale" && r.Str("authKey") == "" {
		return 15 * time.Minute
	}
	return 2 * time.Minute
}

// NetworkUp brings up a network and waits until it is up, it failed, or
// ctx ends (the attempt then continues in the background).
func (h *Hub) NetworkUp(ctx context.Context, w *workspace.Workspace, name string) error {
	r := w.FindRef("network/" + name)
	if r == nil {
		return fmt.Errorf("network %q not found", name)
	}
	_, err := h.networkUp(ctx, w, r)
	return err
}

// ErrConnecting means the network is still connecting (or waiting for a login).
var ErrConnecting = errors.New("still connecting")

func (h *Hub) networkUp(ctx context.Context, w *workspace.Workspace, r *workspace.Resource) (*network, error) {
	key := netKey(w.Name(), r.Name)
	h.mu.Lock()
	n := h.networks[key]
	h.mu.Unlock()
	if n == nil || n.state == "error" {
		items := map[string]vault.Item{}
		for _, f := range secretFields(r) {
			it, err := h.credential(ctx, w, r.Str(f))
			if err != nil {
				return nil, fmt.Errorf("network %s: %w", r.Name, err)
			}
			items[f] = it
		}
		h.mu.Lock()
		if n = h.networks[key]; n == nil || n.state == "error" {
			n = h.launch(ActorFrom(ctx), w, r, items)
			h.networks[key] = n
		}
		h.mu.Unlock()
	}
	select {
	case <-n.ready:
	case <-ctx.Done():
		h.mu.Lock()
		auth := n.authURL
		h.mu.Unlock()
		if auth != "" {
			return nil, fmt.Errorf("network %s: %w — waiting for login at %s", r.Name, ErrConnecting, auth)
		}
		return nil, fmt.Errorf("network %s: %w", r.Name, ErrConnecting)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if n.state != "up" {
		return nil, fmt.Errorf("network %s: %s", r.Name, n.err)
	}
	return n, nil
}

// launch starts connecting in the background. Called with h.mu held.
func (h *Hub) launch(actor Actor, w *workspace.Workspace, r *workspace.Resource, items map[string]vault.Item) *network {
	key := netKey(w.Name(), r.Name)
	res := *r
	mode := vpn.ModeOf(r)
	n := &network{kind: r.Kind, mode: mode, state: "connecting", since: time.Now(), ready: make(chan struct{})}
	// the tunnel outlives the request that started it, but keeps its actor
	ctx, cancel := context.WithTimeout(WithActor(context.Background(), actor), connectTimeout(r))
	n.cancel = cancel
	log := h.netLog(key)
	log.Printf("— connecting %s (%s) —", r.Name, firstNonEmpty(mode, "proxy"))
	ws := w.Name()
	go func() {
		defer cancel()
		tun, dial, err := h.startNetwork(ctx, w, &res, mode, items, log, func(url string) {
			h.mu.Lock()
			n.authURL = url
			h.mu.Unlock()
		})
		h.mu.Lock()
		defer h.mu.Unlock()
		if h.networks[key] != n { // taken down while connecting
			if tun != nil {
				go tun.Close()
			}
			return
		}
		if err != nil {
			n.state, n.err = "error", err.Error()
			log.Printf("failed: %v", err)
			close(n.ready)
			return
		}
		n.tun, n.dial, n.state, n.authURL, n.since = tun, dial, "up", "", time.Now()
		if tun != nil {
			if lp, ok := tun.(interface{ LocalProxy() string }); ok && lp.LocalProxy() != "" {
				n.proxy = lp.LocalProxy()
			} else if port := res.Int("proxyPort", 0); port > 0 && mode == vpn.ModeUserspace {
				if s, err := vpn.ServeSOCKS(port, tun.Dial); err != nil {
					log.Printf("%v", err)
				} else {
					n.socks, n.proxy = s, s.Addr()
					log.Printf("SOCKS5 proxy for other apps on %s", n.proxy)
				}
			}
			go h.watch(ws, key, n)
		}
		log.Printf("connected")
		close(n.ready)
	}()
	return n
}

// watch marks a network as failed when its tunnel stops by itself.
func (h *Hub) watch(ws, key string, n *network) {
	<-n.tun.Done()
	h.mu.Lock()
	still := h.networks[key] == n
	if still {
		n.state, n.err = "error", "connection lost: "+fmt.Sprint(n.tun.Err())
		h.netLog(key).Printf("%s", n.err)
	}
	h.mu.Unlock()
	if still {
		if n.socks != nil {
			n.socks.Close()
		}
		h.dropBroken(ws)
	}
}

func (h *Hub) startNetwork(ctx context.Context, w *workspace.Workspace, r *workspace.Resource, mode string, items map[string]vault.Item, log *vpn.Log, authURL func(string)) (vpn.Tunnel, DialFunc, error) {
	if r.Kind == "SocksProxy" {
		forward, err := h.Dialer(ctx, w, r)
		if err != nil {
			return nil, nil, err
		}
		var auth *proxy.Auth
		if it, ok := items["auth"]; ok {
			auth = &proxy.Auth{User: it.Get("username"), Password: it.Get("password")}
		}
		d, err := proxy.SOCKS5("tcp", r.Str("address"), auth, ctxDialer(forward))
		if err != nil {
			return nil, nil, err
		}
		return nil, d.(proxy.ContextDialer).DialContext, nil
	}
	d, ok := vpn.Get(r.Kind)
	if !ok {
		return nil, nil, fmt.Errorf("%s is not a network", r.Kind)
	}
	elevate := h.elevate()
	if miss := vpn.Missing(d, r, mode, elevate); miss != "" {
		return nil, nil, fmt.Errorf("%s %s mode can't run on this machine: %s", d.Label, mode, miss)
	}
	stateDir := filepath.Join(h.stateDir(w.Name()), "vpn", r.Name)
	runDir := filepath.Join(stateDir, "run")
	if err := os.MkdirAll(runDir, 0o700); err != nil {
		return nil, nil, err
	}
	cfg := &vpn.Config{Res: r, Mode: mode, Items: items, StateDir: stateDir, RunDir: runDir, Elevate: elevate, Log: log, AuthURL: authURL}
	tun, err := d.Start(ctx, cfg)
	if err != nil {
		return nil, nil, err
	}
	return tun, tun.Dial, nil
}

type ctxDialer DialFunc

func (d ctxDialer) Dial(network, addr string) (net.Conn, error) {
	return d(context.Background(), network, addr)
}
func (d ctxDialer) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	return d(ctx, network, addr)
}

// NetworkDown stops a network and drops SSH connections that used it.
func (h *Hub) NetworkDown(ws, name string) {
	key := netKey(ws, name)
	h.mu.Lock()
	n := h.networks[key]
	delete(h.networks, key)
	if n != nil {
		h.netLog(key).Printf("— disconnecting —")
	}
	h.mu.Unlock()
	if n != nil {
		n.stop()
	}
	h.dropBroken(ws)
}

// NetStatus is reported to the UI and the agent.
type NetStatus struct {
	Name          string    `json:"name"`
	Kind          string    `json:"kind"`
	Mode          string    `json:"mode"`   // userspace | system | "" (proxy)
	Status        string    `json:"status"` // down | connecting | auth | up | error
	Error         string    `json:"error,omitempty"`
	AuthURL       string    `json:"authUrl,omitempty"`
	Since         time.Time `json:"since,omitempty"`
	Address       string    `json:"address,omitempty"`
	Interface     string    `json:"interface,omitempty"`
	Server        string    `json:"server,omitempty"`
	Detail        string    `json:"detail,omitempty"`
	Proxy         string    `json:"proxy,omitempty"`
	LastHandshake time.Time `json:"lastHandshake,omitempty"`
	RxBytes       int64     `json:"rxBytes"`
	TxBytes       int64     `json:"txBytes"`
	Missing       string    `json:"missing,omitempty"` // what this machine lacks for the profile's mode
	UsedBy        []string  `json:"usedBy"`            // resources reached through it
}

func (h *Hub) Networks(w *workspace.Workspace) []NetStatus {
	type item struct {
		st  NetStatus
		tun vpn.Tunnel
	}
	var items []item
	elevate := h.elevate()
	h.mu.Lock()
	for _, r := range w.Resources {
		if !workspace.IsNetworkKind(r.Kind) {
			continue
		}
		st := NetStatus{Name: r.Name, Kind: r.Kind, Mode: vpn.ModeOf(r), Status: "down", UsedBy: []string{}}
		if r.Kind == "SocksProxy" {
			st.Address = r.Str("address")
		}
		if d, ok := vpn.Get(r.Kind); ok {
			st.Missing = vpn.Missing(d, r, st.Mode, elevate)
		}
		var tun vpn.Tunnel
		if n := h.networks[netKey(w.Name(), r.Name)]; n != nil {
			st.Status, st.Error, st.Since, st.Proxy = n.state, n.err, n.since, n.proxy
			if n.state == "connecting" && n.authURL != "" {
				st.Status, st.AuthURL = "auth", n.authURL
			}
			tun = n.tun
		}
		for _, x := range w.Resources {
			if x.Via == "network/"+r.Name {
				st.UsedBy = append(st.UsedBy, x.ID())
			}
		}
		items = append(items, item{st, tun})
	}
	h.mu.Unlock()
	out := make([]NetStatus, 0, len(items))
	for _, it := range items {
		if it.tun != nil && it.st.Status == "up" {
			i := it.tun.Info()
			it.st.Address, it.st.Interface, it.st.Server, it.st.Detail = i.Address, i.Interface, i.Server, i.Detail
			it.st.LastHandshake, it.st.RxBytes, it.st.TxBytes = i.LastHandshake, i.RxBytes, i.TxBytes
		}
		out = append(out, it.st)
	}
	return out
}

// NetworkLog returns the last lines of a network's log.
func (h *Hub) NetworkLog(ws, name string, lines int) []vpn.LogLine {
	h.mu.Lock()
	l := h.netLogs[netKey(ws, name)]
	h.mu.Unlock()
	if l == nil {
		return []vpn.LogLine{}
	}
	return l.Lines(lines)
}

func firstNonEmpty(s ...string) string {
	for _, x := range s {
		if x != "" {
			return x
		}
	}
	return ""
}
