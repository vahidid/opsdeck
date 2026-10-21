package vpn

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/netbirdio/netbird/client/embed"

	"opsdeck/internal/workspace"
)

// NetBird joins a NetBird network with the official embeddable client. In
// userspace mode the WireGuard interface and TCP/IP stack live inside
// opsdeck; in system mode the client creates a kernel interface with routes
// and DNS (opsdeck itself then needs root or CAP_NET_ADMIN). The peer's
// config (keys) is kept in the profile's state folder.

func init() {
	register(&Driver{
		Kind: "NetBird", Label: "NetBird", Modes: []string{ModeUserspace, ModeSystem},
		Needs: func(*workspace.Resource, string) []string { return nil },
		Start: startNetBird, InProcessSystem: true,
	})
}

// The embedded client configures itself through process-wide settings
// (environment, logger), so clients start one at a time and their log is
// copied to every running NetBird profile.
var (
	nbStartMu sync.Mutex
	nbLogs    = &fanout{logs: map[*Log]bool{}}
)

type fanout struct {
	mu   sync.Mutex
	logs map[*Log]bool
}

func (f *fanout) Write(p []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for l := range f.logs {
		l.Write(p)
	}
	return len(p), nil
}

func (f *fanout) set(l *Log, on bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if on {
		f.logs[l] = true
	} else {
		delete(f.logs, l)
	}
}

type nbTunnel struct {
	c      *embed.Client
	cfg    *Config
	system bool
	done   *doneOnce

	mu     sync.Mutex
	info   Info
	statAt time.Time
}

func startNetBird(ctx context.Context, cfg *Config) (Tunnel, error) {
	r := cfg.Res
	hostname := r.Str("hostname")
	if hostname == "" {
		hostname = "opsdeck-" + r.Name
	}
	system := cfg.Mode == ModeSystem
	if system && !Privileged() {
		return nil, fmt.Errorf("NetBird system mode runs inside opsdeck, which needs root or CAP_NET_ADMIN; use userspace mode or run: sudo setcap cap_net_admin,cap_net_raw+ep %s", os.Args[0])
	}
	nbStartMu.Lock()
	defer nbStartMu.Unlock()
	if system {
		os.Unsetenv("NB_USE_NETSTACK_MODE")
		os.Unsetenv("NB_NETSTACK_SKIP_PROXY")
	}
	nbLogs.set(cfg.Log, true)
	mgmt := r.Str("managementUrl")
	c, err := embed.New(embed.Options{
		DeviceName:    hostname,
		SetupKey:      cfg.Value("setupKey", "token"),
		ManagementURL: mgmt,
		LogOutput:     nbLogs,
		LogLevel:      "info",
		NoUserspace:   system,
		ConfigPath:    filepath.Join(cfg.StateDir, "config.json"),
		StatePath:     filepath.Join(cfg.StateDir, "state.json"),
	})
	if err != nil {
		nbLogs.set(cfg.Log, false)
		return nil, err
	}
	if mgmt == "" {
		mgmt = "NetBird cloud"
	}
	cfg.Log.Printf("starting NetBird peer %s (management: %s, %s mode)", hostname, mgmt, cfg.Mode)
	if err := c.Start(ctx); err != nil {
		nbLogs.set(cfg.Log, false)
		if strings.Contains(err.Error(), "setup key") || strings.Contains(err.Error(), "PermissionDenied") || strings.Contains(err.Error(), "Unauthenticated") {
			return nil, fmt.Errorf("%w — check the profile's setup key (a vault token) and management URL", err)
		}
		return nil, err
	}
	t := &nbTunnel{c: c, cfg: cfg, system: system, done: newDone()}
	t.info.Server = mgmt
	t.Info()
	cfg.Log.Printf("up: %s", t.info.Address)
	return t, nil
}

func (t *nbTunnel) Dial(ctx context.Context, network, addr string) (net.Conn, error) {
	if t.system {
		return direct(ctx, network, addr)
	}
	return t.c.DialContext(ctx, network, addr)
}

func (t *nbTunnel) Info() Info {
	t.mu.Lock()
	defer t.mu.Unlock()
	if time.Since(t.statAt) > 3*time.Second {
		t.statAt = time.Now()
		if st, err := t.c.Status(); err == nil {
			ip := st.LocalPeerState.IP
			if i := strings.IndexByte(ip, '/'); i > 0 {
				ip = ip[:i]
			}
			t.info.Address = ip
			if st.LocalPeerState.KernelInterface {
				t.info.Interface = "netbird"
			}
			connected := 0
			t.info.RxBytes, t.info.TxBytes, t.info.LastHandshake = 0, 0, time.Time{}
			for _, p := range st.Peers {
				if p.ConnStatus.String() == "Connected" {
					connected++
				}
				t.info.RxBytes += p.BytesRx
				t.info.TxBytes += p.BytesTx
				if p.LastWireguardHandshake.After(t.info.LastHandshake) {
					t.info.LastHandshake = p.LastWireguardHandshake
				}
			}
			mg := "management connected"
			if !st.ManagementState.Connected {
				mg = "management disconnected"
			}
			t.info.Detail = fmt.Sprintf("%d/%d peers connected · %s", connected, len(st.Peers), mg)
		}
	}
	return t.info
}

func (t *nbTunnel) Done() <-chan struct{} { return t.done.Done() }
func (t *nbTunnel) Err() error            { return t.done.Err() }

func (t *nbTunnel) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	err := t.c.Stop(ctx)
	nbLogs.set(t.cfg.Log, false)
	t.done.finish(ErrStopped)
	return err
}
