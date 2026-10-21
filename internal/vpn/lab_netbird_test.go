package vpn

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/netbirdio/netbird/client/embed"

	"opsdeck/internal/vault"
	"opsdeck/internal/workspace"
)

var raceEnabled bool

// TestLabNetBird joins a real self-hosted NetBird server. Run with
// OPSDECK_NETBIRD=<management url>,<reusable setup key>.
func TestLabNetBird(t *testing.T) {
	env := os.Getenv("OPSDECK_NETBIRD")
	if env == "" {
		t.Skip("set OPSDECK_NETBIRD to run against a NetBird server")
	}
	if raceEnabled {
		// The NetBird client itself has data races (peer.Conn / WorkerRelay
		// in v0.81.3) that the race detector reports; opsdeck's side is
		// covered by the other tests.
		t.Skip("upstream NetBird client is not race-clean")
	}
	mgmt, key, _ := strings.Cut(env, ",")
	for _, k := range []string{"https_proxy", "HTTPS_PROXY", "http_proxy", "HTTP_PROXY"} {
		t.Setenv(k, "")
		os.Unsetenv(k)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	// another peer of the network, serving a web page
	dir := t.TempDir()
	web, err := embed.New(embed.Options{DeviceName: "lab-web", SetupKey: key, ManagementURL: mgmt,
		ConfigPath: dir + "/config.json", StatePath: dir + "/state.json", LogOutput: NewLog()})
	if err != nil {
		t.Fatal(err)
	}
	if err := web.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer web.Stop(context.Background())
	ln, err := web.ListenTCP(":8080")
	if err != nil {
		t.Fatal(err)
	}
	go http.Serve(ln, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "hello from netbird") }))
	st, _ := web.Status()
	ip, _, _ := strings.Cut(st.LocalPeerState.IP, "/")
	target := net.JoinHostPort(ip, "8080")

	for _, mode := range []string{ModeUserspace, ModeSystem} {
		t.Run(mode, func(t *testing.T) {
			d, _ := Get("NetBird")
			log := NewLog()
			cfg := &Config{Res: &workspace.Resource{Kind: "NetBird", Name: "lab-" + mode, Spec: map[string]any{"managementUrl": mgmt, "mode": mode}},
				Mode: mode, StateDir: t.TempDir(), Log: log,
				Items: map[string]vault.Item{"setupKey": {Fields: []vault.Field{{Name: "token", Value: key, Secret: true}}}}}
			tun, err := d.Start(ctx, cfg)
			if err != nil {
				t.Fatalf("%v\n%s", err, log.Tail(30))
			}
			defer tun.Close()
			var body string
			for i := 0; i < 20; i++ { // peers connect lazily
				if body, err = httpGet(ctx, tun.Dial, target); err == nil && strings.Contains(body, "hello from netbird") {
					break
				}
				time.Sleep(time.Second)
			}
			if !strings.Contains(body, "hello from netbird") {
				t.Fatalf("through netbird: %q %v\ninfo %+v\n%s", body, err, tun.Info(), log.Tail(30))
			}
			t.Logf("info: %+v", tun.Info())
		})
	}
}
