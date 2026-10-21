package vpn

import (
	"context"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	"tailscale.com/client/local"
	"tailscale.com/envknob"
	"tailscale.com/ipn"
	"tailscale.com/tsnet"

	"opsdeck/internal/workspace"
)

// Tailscale joins a tailnet (Tailscale or a self-hosted Headscale control
// server) with tsnet: a full Tailscale node inside opsdeck with its own
// userspace network stack. The node key is kept in the profile's state
// folder, so it reconnects as the same machine. Without an auth key the
// user logs in through the link shown in the UI.

func init() {
	register(&Driver{
		Kind: "Tailscale", Label: "Tailscale / Headscale", Modes: []string{ModeUserspace},
		Needs: func(*workspace.Resource, string) []string { return nil },
		Start: startTailscale,
	})
}

type tsTunnel struct {
	s      *tsnet.Server
	lc     *local.Client
	cancel context.CancelFunc
	done   *doneOnce
	server string

	mu     sync.Mutex
	info   Info
	statAt time.Time
}

func startTailscale(ctx context.Context, cfg *Config) (Tunnel, error) {
	envknob.SetNoLogsNoSupport() // no log upload to Tailscale's servers
	r := cfg.Res
	hostname := r.Str("hostname")
	if hostname == "" {
		hostname = "opsdeck-" + r.Name
	}
	server := r.Str("controlUrl")
	s := &tsnet.Server{
		Dir:        cfg.StateDir,
		Hostname:   hostname,
		AuthKey:    cfg.Value("authKey", "token"),
		ControlURL: server,
		Ephemeral:  r.Bool("ephemeral", false),
		UserLogf:   func(f string, a ...any) { cfg.Log.Printf(f, a...) },
		Logf:       func(string, ...any) {},
	}
	if server == "" {
		server = "Tailscale"
	}
	cfg.Log.Printf("starting Tailscale node %s (control: %s)", hostname, server)
	lc, err := s.LocalClient()
	if err != nil {
		return nil, err
	}
	wctx, cancel := context.WithCancel(context.Background())
	t := &tsTunnel{s: s, lc: lc, cancel: cancel, done: newDone(), server: server}
	go func() {
		w, err := lc.WatchIPNBus(wctx, ipn.NotifyInitialState)
		if err != nil {
			return
		}
		defer w.Close()
		for {
			n, err := w.Next()
			if err != nil {
				return
			}
			if n.BrowseToURL != nil && *n.BrowseToURL != "" {
				cfg.Log.Printf("login required: open %s", *n.BrowseToURL)
				if cfg.AuthURL != nil {
					cfg.AuthURL(*n.BrowseToURL)
				}
			}
			if n.State != nil {
				cfg.Log.Printf("state: %s", n.State.String())
			}
			if n.ErrMessage != nil {
				cfg.Log.Printf("error: %s", *n.ErrMessage)
			}
		}
	}()
	st, err := s.Up(ctx)
	if err != nil {
		t.Close()
		return nil, err
	}
	if r.Bool("acceptRoutes", true) {
		if _, err := lc.EditPrefs(ctx, &ipn.MaskedPrefs{RouteAllSet: true, Prefs: ipn.Prefs{RouteAll: true}}); err != nil {
			cfg.Log.Printf("accept routes: %v", err)
		}
	}
	if len(st.TailscaleIPs) > 0 {
		t.info.Address = st.TailscaleIPs[0].String()
	}
	if st.CurrentTailnet != nil {
		t.info.Detail = "tailnet " + st.CurrentTailnet.Name
	}
	t.info.Server = server
	cfg.Log.Printf("up: %s", t.info.Address)
	return t, nil
}

func (t *tsTunnel) Dial(ctx context.Context, network, addr string) (net.Conn, error) {
	return t.s.Dial(ctx, network, addr)
}

func (t *tsTunnel) Info() Info {
	t.mu.Lock()
	defer t.mu.Unlock()
	if time.Since(t.statAt) > 3*time.Second {
		t.statAt = time.Now()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		st, err := t.lc.Status(ctx)
		cancel()
		if err == nil {
			var rx, tx int64
			online, total := 0, 0
			for _, p := range st.Peer {
				total++
				if p.Online {
					online++
				}
				rx += p.RxBytes
				tx += p.TxBytes
			}
			t.info.RxBytes, t.info.TxBytes = rx, tx
			parts := []string{}
			if st.CurrentTailnet != nil {
				parts = append(parts, "tailnet "+st.CurrentTailnet.Name)
			}
			parts = append(parts, fmt.Sprintf("%d/%d peers online", online, total))
			if st.BackendState != "Running" {
				parts = append(parts, strings.ToLower(st.BackendState))
			}
			t.info.Detail = strings.Join(parts, " · ")
		}
	}
	return t.info
}

func (t *tsTunnel) Done() <-chan struct{} { return t.done.Done() }
func (t *tsTunnel) Err() error            { return t.done.Err() }

func (t *tsTunnel) Close() error {
	t.cancel()
	err := t.s.Close()
	t.done.finish(ErrStopped)
	return err
}
