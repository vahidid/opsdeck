package vpn

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.zx2c4.com/wireguard/conn"
	"golang.zx2c4.com/wireguard/device"
	"golang.zx2c4.com/wireguard/tun/netstack"

	"opsdeck/internal/workspace"
)

// wgConfig is a parsed wg-quick configuration.
type wgConfig struct {
	PrivateKey string
	Addresses  []netip.Addr
	DNS        []netip.Addr
	MTU        int
	Peers      []wgPeer
}

type wgPeer struct {
	PublicKey    string
	PresharedKey string
	Endpoint     string
	AllowedIPs   []string
	Keepalive    int
}

func parseWGConfig(text string) (*wgConfig, error) {
	cfg := &wgConfig{MTU: 1420}
	section := ""
	var peer *wgPeer
	sc := bufio.NewScanner(strings.NewReader(text))
	for line := 1; sc.Scan(); line++ {
		l := strings.TrimSpace(sc.Text())
		if i := strings.IndexAny(l, "#;"); i >= 0 {
			l = strings.TrimSpace(l[:i])
		}
		if l == "" {
			continue
		}
		if strings.HasPrefix(l, "[") {
			section = strings.ToLower(strings.Trim(l, "[]"))
			if section == "peer" {
				cfg.Peers = append(cfg.Peers, wgPeer{})
				peer = &cfg.Peers[len(cfg.Peers)-1]
			}
			continue
		}
		k, v, ok := strings.Cut(l, "=")
		if !ok {
			return nil, fmt.Errorf("line %d: expected key = value", line)
		}
		k, v = strings.ToLower(strings.TrimSpace(k)), strings.TrimSpace(v)
		switch section {
		case "interface":
			switch k {
			case "privatekey":
				cfg.PrivateKey = v
			case "address":
				for _, a := range splitList(v) {
					p, err := netip.ParsePrefix(a)
					if err != nil {
						addr, err2 := netip.ParseAddr(a)
						if err2 != nil {
							return nil, fmt.Errorf("line %d: bad address %q", line, a)
						}
						cfg.Addresses = append(cfg.Addresses, addr)
						continue
					}
					cfg.Addresses = append(cfg.Addresses, p.Addr())
				}
			case "dns":
				for _, a := range splitList(v) {
					if addr, err := netip.ParseAddr(a); err == nil {
						cfg.DNS = append(cfg.DNS, addr)
					}
				}
			case "mtu":
				if n, err := strconv.Atoi(v); err == nil {
					cfg.MTU = n
				}
			}
		case "peer":
			switch k {
			case "publickey":
				peer.PublicKey = v
			case "presharedkey":
				peer.PresharedKey = v
			case "endpoint":
				peer.Endpoint = v
			case "allowedips":
				peer.AllowedIPs = append(peer.AllowedIPs, splitList(v)...)
			case "persistentkeepalive":
				peer.Keepalive, _ = strconv.Atoi(v)
			}
		}
	}
	if cfg.PrivateKey == "" {
		return nil, fmt.Errorf("[Interface] PrivateKey is missing")
	}
	if len(cfg.Addresses) == 0 {
		return nil, fmt.Errorf("[Interface] Address is missing")
	}
	if len(cfg.Peers) == 0 {
		return nil, fmt.Errorf("no [Peer] section")
	}
	return cfg, nil
}

func splitList(v string) []string {
	var out []string
	for _, p := range strings.Split(v, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func keyHex(b64 string) (string, error) {
	b, err := base64.StdEncoding.DecodeString(b64)
	if err != nil || len(b) != 32 {
		return "", fmt.Errorf("invalid WireGuard key")
	}
	return hex.EncodeToString(b), nil
}

func (c *wgConfig) uapi(ctx context.Context) (string, error) {
	var b strings.Builder
	k, err := keyHex(c.PrivateKey)
	if err != nil {
		return "", fmt.Errorf("PrivateKey: %w", err)
	}
	fmt.Fprintf(&b, "private_key=%s\nreplace_peers=true\n", k)
	for _, p := range c.Peers {
		pk, err := keyHex(p.PublicKey)
		if err != nil {
			return "", fmt.Errorf("peer PublicKey: %w", err)
		}
		fmt.Fprintf(&b, "public_key=%s\n", pk)
		if p.PresharedKey != "" {
			psk, err := keyHex(p.PresharedKey)
			if err != nil {
				return "", fmt.Errorf("PresharedKey: %w", err)
			}
			fmt.Fprintf(&b, "preshared_key=%s\n", psk)
		}
		if p.Endpoint != "" {
			host, port, err := net.SplitHostPort(p.Endpoint)
			if err != nil {
				return "", fmt.Errorf("endpoint %q: %w", p.Endpoint, err)
			}
			ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
			if err != nil || len(ips) == 0 {
				return "", fmt.Errorf("resolve endpoint %s: %v", host, err)
			}
			ip := ips[0]
			for _, cand := range ips {
				if cand.Is4() {
					ip = cand
					break
				}
			}
			fmt.Fprintf(&b, "endpoint=%s\n", net.JoinHostPort(ip.Unmap().String(), port))
		}
		if p.Keepalive > 0 {
			fmt.Fprintf(&b, "persistent_keepalive_interval=%d\n", p.Keepalive)
		}
		for _, a := range p.AllowedIPs {
			fmt.Fprintf(&b, "allowed_ip=%s\n", a)
		}
	}
	return b.String(), nil
}

// wgTunnel is a userspace WireGuard interface with its own TCP/IP stack:
// no root, no system routes, and many can run side by side.
type wgTunnel struct {
	dev  *device.Device
	tnet *netstack.Net
	addr string
	done *doneOnce
}

func startWireGuard(ctx context.Context, confText string) (*wgTunnel, error) {
	cfg, err := parseWGConfig(confText)
	if err != nil {
		return nil, err
	}
	ipc, err := cfg.uapi(ctx)
	if err != nil {
		return nil, err
	}
	tunDev, tnet, err := netstack.CreateNetTUN(cfg.Addresses, cfg.DNS, cfg.MTU)
	if err != nil {
		return nil, err
	}
	logger := &device.Logger{Verbosef: device.DiscardLogf, Errorf: device.DiscardLogf}
	dev := device.NewDevice(tunDev, conn.NewDefaultBind(), logger)
	if err := dev.IpcSet(ipc); err != nil {
		dev.Close()
		return nil, fmt.Errorf("configure wireguard: %w", err)
	}
	if err := dev.Up(); err != nil {
		dev.Close()
		return nil, err
	}
	return &wgTunnel{dev: dev, tnet: tnet, addr: cfg.Addresses[0].String(), done: newDone()}, nil
}

func (t *wgTunnel) Dial(ctx context.Context, network, addr string) (net.Conn, error) {
	return t.tnet.DialContext(ctx, network, addr)
}

func (t *wgTunnel) Close() error {
	t.dev.Close()
	t.done.finish(ErrStopped)
	return nil
}

func (t *wgTunnel) Done() <-chan struct{} { return t.done.Done() }
func (t *wgTunnel) Err() error            { return t.done.Err() }

func (t *wgTunnel) Info() Info {
	hs, rx, tx := t.stats()
	return Info{Address: t.addr, LastHandshake: hs, RxBytes: rx, TxBytes: tx}
}

// stats returns the most recent handshake and transfer counters.
func (t *wgTunnel) stats() (lastHandshake time.Time, rx, tx int64) {
	s, err := t.dev.IpcGet()
	if err != nil {
		return
	}
	var sec int64
	for _, line := range strings.Split(s, "\n") {
		k, v, _ := strings.Cut(line, "=")
		n, _ := strconv.ParseInt(v, 10, 64)
		switch k {
		case "last_handshake_time_sec":
			if n > sec {
				sec = n
			}
		case "rx_bytes":
			rx += n
		case "tx_bytes":
			tx += n
		}
	}
	if sec > 0 {
		lastHandshake = time.Unix(sec, 0)
	}
	return
}

// ---------------------------------------------------------------------------
// Driver

func init() {
	register(&Driver{
		Kind: "WireGuard", Label: "WireGuard", Modes: []string{ModeUserspace, ModeSystem},
		Needs: func(r *workspace.Resource, mode string) []string {
			if mode == ModeSystem {
				return []string{"wg-quick", "wg"}
			}
			return nil
		},
		Start: func(ctx context.Context, cfg *Config) (Tunnel, error) {
			conf := cfg.Value("config", "config")
			if conf == "" {
				return nil, fmt.Errorf("no WireGuard config: set the profile's config to a vault item of type VPN config")
			}
			if cfg.Mode == ModeSystem {
				return startWGQuick(ctx, cfg, conf)
			}
			cfg.Log.Printf("starting userspace WireGuard")
			t, err := startWireGuard(ctx, conf)
			if err != nil {
				return nil, err
			}
			cfg.Log.Printf("up: %s", t.addr)
			return t, nil
		},
	})
}

// wgQuick is a kernel/system WireGuard interface managed by wg-quick.
type wgQuick struct {
	cfg   *Config
	iface string
	path  string
	addr  string
	done  *doneOnce
	stop  chan struct{}

	mu      sync.Mutex
	statsAt time.Time
	hs      time.Time
	rx, tx  int64
}

func startWGQuick(ctx context.Context, cfg *Config, conf string) (Tunnel, error) {
	parsed, err := parseWGConfig(conf)
	if err != nil {
		return nil, err
	}
	iface := ifaceName("od-", cfg.Res.Name)
	path := filepath.Join(cfg.RunDir, iface+".conf")
	if err := writePrivate(path, conf); err != nil {
		return nil, err
	}
	if _, err := run(ctx, []string{"wg-quick", "up", path}, cfg.Elevate, cfg.Log); err != nil {
		os.Remove(path)
		return nil, err
	}
	t := &wgQuick{cfg: cfg, iface: iface, path: path, addr: parsed.Addresses[0].String(), done: newDone(), stop: make(chan struct{})}
	if runtime.GOOS == "darwin" { // wg-quick runs the tunnel on a utunN of macOS's choosing
		if name := ifaceWithAddr(t.addr); name != "" {
			t.iface = name
		}
	}
	go t.watch()
	return t, nil
}

// watch notices when the interface disappears (e.g. removed by hand).
func (t *wgQuick) watch() {
	tick := time.NewTicker(10 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-t.stop:
			return
		case <-tick.C:
			if _, err := net.InterfaceByName(t.iface); err != nil {
				t.done.finish(fmt.Errorf("interface %s is gone", t.iface))
				return
			}
		}
	}
}

func (t *wgQuick) Dial(ctx context.Context, network, addr string) (net.Conn, error) {
	return direct(ctx, network, addr)
}

func (t *wgQuick) Info() Info {
	t.mu.Lock()
	defer t.mu.Unlock()
	if time.Since(t.statsAt) > 5*time.Second {
		t.statsAt = time.Now()
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		out, err := run(ctx, []string{"wg", "show", t.iface, "dump"}, nonInteractive(t.cfg.Elevate), NewLog())
		cancel()
		if err == nil {
			t.hs, t.rx, t.tx = time.Time{}, 0, 0
			for i, line := range strings.Split(strings.TrimSpace(out), "\n") {
				f := strings.Split(line, "\t")
				if i == 0 || len(f) < 7 {
					continue
				}
				if sec, _ := strconv.ParseInt(f[4], 10, 64); sec > 0 && time.Unix(sec, 0).After(t.hs) {
					t.hs = time.Unix(sec, 0)
				}
				rx, _ := strconv.ParseInt(f[5], 10, 64)
				tx, _ := strconv.ParseInt(f[6], 10, 64)
				t.rx += rx
				t.tx += tx
			}
		}
	}
	return Info{Address: t.addr, Interface: t.iface, LastHandshake: t.hs, RxBytes: t.rx, TxBytes: t.tx}
}

func (t *wgQuick) Done() <-chan struct{} { return t.done.Done() }
func (t *wgQuick) Err() error            { return t.done.Err() }

func (t *wgQuick) Close() error {
	select {
	case <-t.stop:
		return nil
	default:
		close(t.stop)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	_, err := run(ctx, []string{"wg-quick", "down", t.path}, t.cfg.Elevate, t.cfg.Log)
	os.Remove(t.path)
	t.done.finish(ErrStopped)
	return err
}
