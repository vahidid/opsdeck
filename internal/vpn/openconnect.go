package vpn

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/proxy"

	"opsdeck/internal/workspace"
)

// OpenConnect drives the openconnect binary, one client for Cisco AnyConnect
// (and ocserv), FortiGate, Palo Alto GlobalProtect, Pulse/Ivanti, Juniper,
// F5 and Array. In userspace mode openconnect hands packets to ocproxy
// (--script-tun), which serves a SOCKS5 proxy on 127.0.0.1: no root and no
// system change. In system mode it creates a TUN interface configured by
// vpnc-script. Credentials go through stdin, never the command line; a TOTP
// secret in the login item answers OTP prompts (--token-mode=totp); SAML/SSO
// gateways use a session cookie obtained with `openconnect --authenticate`.

var ocProtocols = []string{"anyconnect", "fortinet", "gp", "pulse", "nc", "f5", "array"}

func init() {
	register(&Driver{
		Kind: "OpenConnect", Label: "OpenConnect (AnyConnect, ocserv, FortiGate, GlobalProtect, Pulse, F5)", Modes: []string{ModeUserspace, ModeSystem},
		Needs: func(r *workspace.Resource, mode string) []string {
			if mode == ModeUserspace {
				return []string{"openconnect", "ocproxy"}
			}
			return []string{"openconnect"}
		},
		Start: startOpenConnect,
	})
	register(&Driver{
		Kind: "OpenFortiVPN", Label: "FortiGate (openfortivpn)", Modes: []string{ModeSystem},
		Needs: func(*workspace.Resource, string) []string { return []string{"openfortivpn"} },
		Start: startOpenFortiVPN,
	})
}

var (
	ocConnectedRe = regexp.MustCompile(`(?:Connected as|Configured as) ([0-9a-fA-F.:]+)`)
	ocPinRe       = regexp.MustCompile(`--servercert (pin-sha256:[A-Za-z0-9+/=]+)`)
)

// cliTunnel is a VPN client subprocess that reports "up" through its output.
type cliTunnel struct {
	cfg   *Config
	p     *proc
	proxy string // userspace: SOCKS5 address served by the helper
	dial  DialFunc

	mu      sync.Mutex
	info    Info
	up      chan struct{}
	upOnce  sync.Once
	hint    string // explanation of a likely failure seen in the output
	cleanup func()
	started chan struct{} // closed once p is set (output callbacks may run before)
}

func (t *cliTunnel) markUp() { t.upOnce.Do(func() { close(t.up) }) }

func (t *cliTunnel) setHint(h string) {
	t.mu.Lock()
	if t.hint == "" {
		t.hint = h
	}
	t.mu.Unlock()
}

func (t *cliTunnel) wait(ctx context.Context, name string) (Tunnel, error) {
	select {
	case <-t.up:
		return t, nil
	case <-t.p.done.Done():
		t.Close()
		t.mu.Lock()
		defer t.mu.Unlock()
		if t.hint != "" {
			return nil, errors.New(t.hint)
		}
		return nil, t.p.done.Err()
	case <-ctx.Done():
		t.Close()
		return nil, fmt.Errorf("%s did not connect: %w", name, ctx.Err())
	}
}

func (t *cliTunnel) Dial(ctx context.Context, network, addr string) (net.Conn, error) {
	return t.dial(ctx, network, addr)
}

func (t *cliTunnel) Info() Info {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.info
}

// LocalProxy is the SOCKS5 address other apps can use (userspace mode).
func (t *cliTunnel) LocalProxy() string { return t.proxy }

func (t *cliTunnel) Done() <-chan struct{} { return t.p.done.Done() }
func (t *cliTunnel) Err() error            { return t.p.done.Err() }

func (t *cliTunnel) Close() error {
	t.p.stop(8 * time.Second)
	if t.cleanup != nil {
		t.cleanup()
	}
	return nil
}

// totpSecret extracts the base32 secret of a TOTP field (plain or otpauth://).
func totpSecret(v string) string {
	if strings.HasPrefix(v, "otpauth://") {
		if u, err := url.Parse(v); err == nil {
			return u.Query().Get("secret")
		}
	}
	return strings.ReplaceAll(v, " ", "")
}

func startOpenConnect(ctx context.Context, cfg *Config) (Tunnel, error) {
	r := cfg.Res
	server := strings.TrimSpace(r.Str("server"))
	if server == "" {
		return nil, errors.New("set the VPN server (e.g. vpn.example.com or https://vpn.example.com/group)")
	}
	protocol := r.Str("protocol")
	if protocol == "" {
		protocol = "anyconnect"
	}
	ok := false
	for _, p := range ocProtocols {
		ok = ok || p == protocol
	}
	if !ok {
		return nil, fmt.Errorf("unknown protocol %q (one of %s)", protocol, strings.Join(ocProtocols, ", "))
	}
	auth := cfg.Items["auth"]
	argv := []string{"openconnect", "--protocol=" + protocol, "--non-inter"}
	stdin := ""
	pin := r.Str("serverCert")
	switch {
	case r.Bool("sso", false):
		res, err := ssoLogin(ctx, cfg, protocol)
		if err != nil {
			return nil, err
		}
		cfg.prompt(AuthPrompt{}) // signed in; back to "connecting"
		cfg.Log.Printf("single sign-on complete; connecting")
		if protocol == "gp" {
			argv = append(argv, "--user="+res.User, "--usergroup="+res.UserGroup, "--passwd-on-stdin")
		} else {
			argv = append(argv, "--cookie-on-stdin")
		}
		stdin = res.Cookie + "\n"
		if pin == "" {
			pin = res.ServerCert
		}
		if res.ConnectURL != "" {
			server = res.ConnectURL
		}
	case cfg.Value("cookie", "token") != "":
		argv = append(argv, "--cookie-on-stdin")
		stdin = cfg.Value("cookie", "token") + "\n"
	default:
		if u := auth.Get("username"); u != "" {
			argv = append(argv, "--user="+u)
		}
		if pw := auth.Get("password"); pw != "" {
			argv = append(argv, "--passwd-on-stdin")
			stdin = pw + "\n"
		}
		if s := totpSecret(auth.Get("totp")); s != "" {
			argv = append(argv, "--token-mode=totp", "--token-secret=base32:"+s)
		}
	}
	if g := r.Str("authGroup"); g != "" && !r.Bool("sso", false) {
		argv = append(argv, "--authgroup="+g)
	}
	if pin != "" {
		argv = append(argv, "--servercert="+pin)
	}
	t := &cliTunnel{cfg: cfg, up: make(chan struct{}), started: make(chan struct{})}
	t.info.Server = server
	elevate := cfg.Elevate
	if cfg.Mode == ModeUserspace {
		port := r.Int("proxyPort", 0)
		if port == 0 {
			p, err := freePort()
			if err != nil {
				return nil, err
			}
			port = p
		}
		t.proxy = net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
		argv = append(argv, "--script-tun", "--script", "ocproxy -D "+strconv.Itoa(port))
		d, err := proxy.SOCKS5("tcp", t.proxy, nil, proxy.Direct)
		if err != nil {
			return nil, err
		}
		t.dial = d.(proxy.ContextDialer).DialContext
		elevate = nil // userspace needs no privileges
	} else {
		iface := ifaceName("oc-", r.Name)
		argv = append(argv, "--interface="+iface)
		t.info.Interface = iface
		t.dial = direct
	}
	argv = append(argv, server)
	var err error
	t.p, err = startProc(argv, elevate, stdin, cfg.Log, func(line string) {
		switch {
		case ocConnectedRe.MatchString(line):
			t.mu.Lock()
			t.info.Address = ocConnectedRe.FindStringSubmatch(line)[1]
			t.mu.Unlock()
			if cfg.Mode == ModeUserspace {
				go t.waitProxy()
			} else {
				go t.waitIface()
			}
		case ocPinRe.MatchString(line):
			t.setHint("the server certificate is not trusted. If it is the right server, set the profile's server certificate to " + ocPinRe.FindStringSubmatch(line)[1])
		case strings.Contains(line, "Login failed") || strings.Contains(line, "authentication failed") || strings.Contains(line, "Invalid password"):
			t.setHint("login failed: check the username/password (and OTP secret) of the profile's login")
		case strings.Contains(line, "SAML") || strings.Contains(line, "single sign-on") || strings.Contains(line, "SSO"):
			t.setHint("the gateway uses SAML/SSO login: run `openconnect --authenticate " + server + "` once in a terminal (or the vendor client) and store the cookie in the vault as a token, then set it as the profile's session cookie")
		case strings.Contains(line, "DTLS") || strings.Contains(line, "ESP session established"):
			t.mu.Lock()
			t.info.Detail = strings.TrimSpace(line)
			t.mu.Unlock()
		}
	})
	if err != nil {
		return nil, err
	}
	close(t.started)
	return t.wait(ctx, "openconnect")
}

// waitProxy marks a userspace tunnel up once ocproxy accepts connections.
func (t *cliTunnel) waitProxy() {
	<-t.started
	for i := 0; i < 50; i++ {
		c, err := net.DialTimeout("tcp", t.proxy, 500*time.Millisecond)
		if err == nil {
			c.Close()
			t.markUp()
			return
		}
		select {
		case <-t.p.done.Done():
			return
		case <-time.After(200 * time.Millisecond):
		}
	}
	t.setHint("ocproxy did not start on " + t.proxy + " (is the port free?)")
	t.p.stop(3 * time.Second)
}

// waitIface marks a system tunnel up once vpnc-script has configured the
// interface (openconnect reports the address before the script runs).
func (t *cliTunnel) waitIface() {
	<-t.started
	t.mu.Lock()
	iface, addr := t.info.Interface, t.info.Address
	t.mu.Unlock()
	for i := 0; i < 100; i++ {
		if ifi, err := net.InterfaceByName(iface); err == nil {
			addrs, _ := ifi.Addrs()
			for _, a := range addrs {
				if strings.HasPrefix(a.String(), addr+"/") {
					time.Sleep(300 * time.Millisecond) // routes follow the address
					t.markUp()
					return
				}
			}
		}
		select {
		case <-t.p.done.Done():
			return
		case <-time.After(200 * time.Millisecond):
		}
	}
	t.cfg.Log.Printf("interface %s did not get %s from vpnc-script; continuing", iface, addr)
	t.markUp()
}

var (
	ofvUpRe   = regexp.MustCompile(`Tunnel is up and running`)
	ofvAddrRe = regexp.MustCompile(`Got addresses: \[([^\]]+)\]`)
	ofvCertRe = regexp.MustCompile(`--trusted-cert ([0-9a-f]{64})`)
	ofvIfRe   = regexp.MustCompile(`Interface (ppp\d+) is UP`)
)

func startOpenFortiVPN(ctx context.Context, cfg *Config) (Tunnel, error) {
	r := cfg.Res
	server := strings.TrimSpace(r.Str("server"))
	if server == "" {
		return nil, errors.New("set the FortiGate address (host or host:port)")
	}
	host := strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(server, "https://"), "http://"), "/")
	port := "443"
	if h, p, err := net.SplitHostPort(host); err == nil {
		host, port = h, p
	}
	auth := cfg.Items["auth"]
	var conf strings.Builder
	fmt.Fprintf(&conf, "host = %s\nport = %s\n", host, port)
	stdin := ""
	sso := r.Bool("sso", false)
	if sso {
		res, err := ssoLogin(ctx, cfg, "fortinet")
		if err != nil {
			return nil, err
		}
		cfg.prompt(AuthPrompt{})
		cfg.Log.Printf("single sign-on complete; connecting")
		stdin = res.Cookie + "\n"
	} else {
		if u := auth.Get("username"); u != "" {
			fmt.Fprintf(&conf, "username = %s\n", u)
		}
		if pw := auth.Get("password"); pw != "" {
			fmt.Fprintf(&conf, "password = %s\n", pw)
		}
	}
	if c := r.Str("serverCert"); c != "" {
		fmt.Fprintf(&conf, "trusted-cert = %s\n", strings.TrimPrefix(c, "sha256:"))
	}
	if g := r.Str("authGroup"); g != "" {
		fmt.Fprintf(&conf, "realm = %s\n", g)
	}
	path := filepath.Join(cfg.RunDir, "openfortivpn.conf")
	if err := writePrivate(path, conf.String()); err != nil {
		return nil, err
	}
	argv := []string{"openfortivpn", "-c", path}
	if sso {
		argv = append(argv, "--cookie-on-stdin")
	} else if auth.Get("totp") != "" {
		code, err := otp(auth)
		if err != nil {
			return nil, err
		}
		argv = append(argv, "--otp="+code)
	}
	t := &cliTunnel{cfg: cfg, up: make(chan struct{}), started: make(chan struct{}), dial: direct, cleanup: func() { removeAll(cfg.RunDir) }}
	t.info.Server = net.JoinHostPort(host, port)
	var err error
	t.p, err = startProc(argv, cfg.Elevate, stdin, cfg.Log, func(line string) {
		switch {
		case ofvAddrRe.MatchString(line):
			t.mu.Lock()
			t.info.Address = ofvAddrRe.FindStringSubmatch(line)[1]
			t.mu.Unlock()
		case ofvIfRe.MatchString(line):
			t.mu.Lock()
			t.info.Interface = ofvIfRe.FindStringSubmatch(line)[1]
			t.mu.Unlock()
		case ofvUpRe.MatchString(line):
			t.markUp()
		case ofvCertRe.MatchString(line):
			t.setHint("the gateway certificate is not trusted. If it is the right gateway, set the profile's server certificate to " + ofvCertRe.FindStringSubmatch(line)[1])
		case strings.Contains(line, "Could not authenticate") || strings.Contains(line, "permission denied"):
			t.setHint("login failed: check the profile's login")
		}
	})
	if err != nil {
		return nil, err
	}
	close(t.started)
	return t.wait(ctx, "openfortivpn")
}
