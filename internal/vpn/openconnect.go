package vpn

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
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
// vpnc-script. Credentials go through stdin, never the command line, and
// opsdeck answers what the gateway asks after the password (a group, a
// one-time code: see ocprompt.go); SAML/SSO gateways use a session cookie
// opsdeck obtains itself (sso.go).

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

// failWith sets the reason the connection fails, over any hint ("" = none).
func (t *cliTunnel) failWith(reason string) {
	t.mu.Lock()
	t.hint = reason
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
	pin := r.Str("serverCert")
	if protocol == "fortinet" && fortiPasswordLogin(cfg) {
		return withFortiSession(ctx, cfg, func(cookie string) (Tunnel, error) {
			return runOpenConnect(ctx, cfg, protocol, server, pin, []string{"--cookie-on-stdin"}, cookie+"\n", nil)
		})
	}
	auth := cfg.Items["auth"]
	var args []string
	stdin := ""
	var answers *ocAnswers
	switch {
	case r.Bool("sso", false):
		res, err := ssoLogin(ctx, cfg, protocol)
		if err != nil {
			return nil, err
		}
		cfg.prompt(AuthPrompt{}) // signed in; back to "connecting"
		cfg.Log.Printf("single sign-on complete; connecting")
		if protocol == "gp" {
			args = append(args, "--user="+res.User, "--usergroup="+res.UserGroup, "--passwd-on-stdin")
		} else {
			args = append(args, "--cookie-on-stdin")
		}
		stdin = res.Cookie + "\n"
		if pin == "" {
			pin = res.ServerCert
		}
		if res.ConnectURL != "" {
			server = res.ConnectURL
		}
	case cfg.Value("cookie", "token") != "":
		args = append(args, "--cookie-on-stdin")
		stdin = cfg.Value("cookie", "token") + "\n"
	default:
		user, pw, secret := auth.Get("username"), auth.Get("password"), totpSecret(auth.Get("totp"))
		if user != "" {
			args = append(args, "--user="+user)
		}
		switch {
		case pw != "":
			// openconnect reads its further answers from stdin too
			args = append(args, "--passwd-on-stdin")
			stdin = pw + "\n"
			answers = &ocAnswers{cfg: cfg, ctx: ctx, user: user, password: pw, group: r.Str("authGroup"), totp: secret}
		case secret != "":
			// no password to answer with: openconnect answers OTP fields itself
			// (in a file, not on the command line)
			if err := os.MkdirAll(cfg.RunDir, 0o700); err != nil {
				return nil, err
			}
			path := filepath.Join(cfg.RunDir, "totp")
			if err := writePrivate(path, "base32:"+secret); err != nil {
				return nil, err
			}
			args = append(args, "--token-mode=totp", "--token-secret=@"+path)
		}
	}
	if g := r.Str("authGroup"); g != "" && !r.Bool("sso", false) {
		args = append(args, "--authgroup="+g)
	}
	return runOpenConnect(ctx, cfg, protocol, server, pin, args, stdin, answers)
}

// runOpenConnect starts openconnect with the given authentication arguments
// and stdin, and waits until the tunnel is up. With answers, opsdeck answers
// the gateway's further questions on stdin.
func runOpenConnect(ctx context.Context, cfg *Config, protocol, server, pin string, auth []string, stdin string, answers *ocAnswers) (Tunnel, error) {
	r := cfg.Res
	argv := append([]string{"openconnect", "--protocol=" + protocol, "--non-inter"}, auth...)
	if pin != "" {
		argv = append(argv, "--servercert="+pin)
	}
	t := &cliTunnel{cfg: cfg, up: make(chan struct{}), started: make(chan struct{}), cleanup: func() { removeAll(cfg.RunDir) }}
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
	var onPrompt func(string)
	if answers != nil {
		answers.t = t
		onPrompt = answers.question
	}
	var err error
	t.p, err = startProcPrompts(argv, elevate, stdin, cfg.Log, func(line string) {
		if answers != nil {
			answers.line(line)
		}
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
		case strings.Contains(line, "User input required in non-interactive mode"):
			t.setHint("the gateway asks for more than the profile gives (see the log): check its group and login (a password lets opsdeck answer the gateway's questions)")
		case strings.Contains(line, "Cookie was rejected") || strings.Contains(line, "Cookie is no longer valid"):
			t.setHint("the gateway did not accept the session: " + strings.TrimSpace(line))
		case strings.Contains(line, "SAML") || strings.Contains(line, "single sign-on") || strings.Contains(line, "SSO"):
			t.setHint("the gateway uses SAML/SSO login: run `openconnect --authenticate " + server + "` once in a terminal (or the vendor client) and store the cookie in the vault as a token, then set it as the profile's session cookie")
		case strings.Contains(line, "DTLS") || strings.Contains(line, "ESP session established"):
			t.mu.Lock()
			t.info.Detail = strings.TrimSpace(line)
			t.mu.Unlock()
		}
	}, onPrompt)
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
	if fortiPasswordLogin(cfg) && ofvCookieOnStdin() {
		return withFortiSession(ctx, cfg, func(cookie string) (Tunnel, error) {
			return runOpenFortiVPN(ctx, cfg, host, port, "", []string{"--cookie-on-stdin"}, cookie+"\n", false)
		})
	}
	var creds strings.Builder
	var args []string
	stdin := ""
	if r.Bool("sso", false) {
		res, err := ssoLogin(ctx, cfg, "fortinet")
		if err != nil {
			return nil, err
		}
		cfg.prompt(AuthPrompt{})
		cfg.Log.Printf("single sign-on complete; connecting")
		args = append(args, "--cookie-on-stdin")
		stdin = res.Cookie + "\n"
	} else {
		// an openfortivpn that cannot take a session cookie logs in itself
		if u := auth.Get("username"); u != "" {
			fmt.Fprintf(&creds, "username = %s\n", u)
		}
		if pw := auth.Get("password"); pw != "" {
			fmt.Fprintf(&creds, "password = %s\n", pw)
		}
		if auth.Get("totp") != "" {
			code, err := otp(auth)
			if err != nil {
				return nil, err
			}
			args = append(args, "--otp="+code)
		}
	}
	return runOpenFortiVPN(ctx, cfg, host, port, creds.String(), args, stdin, !r.Bool("sso", false))
}

// runOpenFortiVPN writes openfortivpn's config (with creds, if it logs in
// itself) and starts it; oldLogin explains a one-time code it cannot ask for.
func runOpenFortiVPN(ctx context.Context, cfg *Config, host, port, creds string, args []string, stdin string, oldLogin bool) (Tunnel, error) {
	r := cfg.Res
	var conf strings.Builder
	fmt.Fprintf(&conf, "host = %s\nport = %s\n%s", host, port, creds)
	if c := r.Str("serverCert"); c != "" {
		fmt.Fprintf(&conf, "trusted-cert = %s\n", strings.TrimPrefix(c, "sha256:"))
	}
	if g := r.Str("authGroup"); g != "" {
		fmt.Fprintf(&conf, "realm = %s\n", g)
	}
	// a failed attempt removed the folder (and another one may follow)
	if err := os.MkdirAll(cfg.RunDir, 0o700); err != nil {
		return nil, err
	}
	path := filepath.Join(cfg.RunDir, "openfortivpn.conf")
	if err := writePrivate(path, conf.String()); err != nil {
		return nil, err
	}
	argv := append([]string{"openfortivpn", "-c", path}, args...)
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
		case oldLogin && (strings.Contains(line, "No token specified") || strings.Contains(line, "Two-factor authentication")):
			t.setHint("the gateway asks for a one-time code (SMS, email or token). This openfortivpn cannot hand the login to opsdeck to ask you for it: install a newer openfortivpn (one with --cookie-on-stdin), use a FortiGate profile with OpenConnect, or add the TOTP secret to the login")
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
