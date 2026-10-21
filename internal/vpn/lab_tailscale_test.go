package vpn

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path"
	"strings"
	"testing"
	"time"

	"tailscale.com/tsnet"

	"opsdeck/internal/vault"
	"opsdeck/internal/workspace"
)

// TestLabTailscale joins a real Headscale control server. Run with
// OPSDECK_HEADSCALE=<server url>,<reusable pre-auth key>,<headscale binary>,<headscale config>.
func TestLabTailscale(t *testing.T) {
	env := os.Getenv("OPSDECK_HEADSCALE")
	if env == "" {
		t.Skip("set OPSDECK_HEADSCALE to run against Headscale")
	}
	parts := strings.Split(env, ",")
	url, key, bin, conf := parts[0], parts[1], parts[2], parts[3]
	for _, k := range []string{"https_proxy", "HTTPS_PROXY", "http_proxy", "HTTP_PROXY"} {
		t.Setenv(k, "")
		os.Unsetenv(k)
	}

	// another machine of the tailnet, serving a web page
	web := &tsnet.Server{Dir: t.TempDir(), Hostname: "lab-web", AuthKey: key, ControlURL: url, Logf: func(string, ...any) {}}
	defer web.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	st, err := web.Up(ctx)
	if err != nil {
		t.Fatal(err)
	}
	ln, err := web.Listen("tcp", ":8080")
	if err != nil {
		t.Fatal(err)
	}
	go http.Serve(ln, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "hello from the tailnet") }))
	target := net.JoinHostPort(st.TailscaleIPs[0].String(), "8080")

	check := func(t *testing.T, tun Tunnel) {
		body, err := httpGet(ctx, tun.Dial, target)
		if err != nil || !strings.Contains(body, "hello from the tailnet") {
			lc, _ := web.LocalClient()
			wst, _ := lc.Status(ctx)
			var peers []string
			for _, p := range wst.Peer {
				peers = append(peers, fmt.Sprintf("%s %v online=%v relay=%s cur=%s", p.HostName, p.TailscaleIPs, p.Online, p.Relay, p.CurAddr))
			}
			t.Fatalf("through the tailnet: %q %v\ninfo: %+v\nweb peers: %v", body, err, tun.Info(), peers)
		}
		t.Logf("info: %+v", tun.Info())
	}

	t.Run("auth-key", func(t *testing.T) {
		d, _ := Get("Tailscale")
		log := NewLog()
		cfg := &Config{Res: &workspace.Resource{Kind: "Tailscale", Name: "lab", Spec: map[string]any{"controlUrl": url}},
			Mode: ModeUserspace, StateDir: t.TempDir(), Log: log,
			Items: map[string]vault.Item{"authKey": {Fields: []vault.Field{{Name: "token", Value: key, Secret: true}}}}}
		tun, err := d.Start(ctx, cfg)
		if err != nil {
			t.Fatalf("%v\n%s", err, log.Tail(30))
		}
		defer tun.Close()
		check(t, tun)
	})

	t.Run("login-link", func(t *testing.T) {
		d, _ := Get("Tailscale")
		log := NewLog()
		urls := make(chan string, 4)
		cfg := &Config{Res: &workspace.Resource{Kind: "Tailscale", Name: "lab2", Spec: map[string]any{"controlUrl": url}},
			Mode: ModeUserspace, StateDir: t.TempDir(), Log: log, AuthURL: func(u string) { urls <- u }}
		type res struct {
			tun Tunnel
			err error
		}
		done := make(chan res, 1)
		go func() { tun, err := d.Start(ctx, cfg); done <- res{tun, err} }()
		var link string
		select {
		case link = <-urls:
		case r := <-done:
			t.Fatalf("started without a login: %v", r.err)
		case <-ctx.Done():
			t.Fatalf("no login link\n%s", log.Tail(30))
		}
		t.Logf("login link: %s", link)
		// the user opens the link; on Headscale an admin approves the node
		out, err := exec.Command(bin, "-c", conf, "nodes", "register", "--user", "lab", "--key", path.Base(link)).CombinedOutput()
		if err != nil {
			t.Fatalf("register: %v %s", err, out)
		}
		r := <-done
		if r.err != nil {
			t.Fatalf("%v\n%s", r.err, log.Tail(30))
		}
		defer r.tun.Close()
		check(t, r.tun)
	})
}
