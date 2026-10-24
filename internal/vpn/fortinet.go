package vpn

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"

	"opsdeck/internal/sysproc"
	"opsdeck/internal/vault"
	"opsdeck/internal/workspace"
)

// FortiGate password logins. opsdeck logs in itself, with the requests
// openfortivpn and openconnect make (POST /remote/logincheck), and starts the
// client with the session cookie (--cookie-on-stdin). When the gateway asks
// for a one-time code (tokeninfo= in its answer: a code it sent by SMS or
// email, or a FortiToken), a TOTP secret on the login answers it; otherwise
// the user is asked (Config.Code), who types the code or has it forwarded
// from the phone. The session is kept (Config.Session), so a tunnel that
// drops connects again without a new code while the gateway keeps it.

const fortiUA = "Mozilla/5.0 SV1" // what openfortivpn and FortiClient send

// MayAskForCode reports whether connecting a profile can wait for a one-time
// code from the user: FortiGate logins with a username and password.
func MayAskForCode(r *workspace.Resource) bool {
	forti := r.Kind == "OpenFortiVPN" || (r.Kind == "OpenConnect" && r.Str("protocol") == "fortinet")
	return forti && !r.Bool("sso", false) && r.Str("cookie") == "" && r.Str("auth") != ""
}

// fortiPasswordLogin reports whether opsdeck logs in to a FortiGate with the
// profile's login item (not SSO, not a stored session cookie).
func fortiPasswordLogin(cfg *Config) bool {
	if cfg.Res.Bool("sso", false) || cfg.Value("cookie", "token") != "" {
		return false
	}
	return cfg.Value("auth", "username") != ""
}

// Session keeps a gateway's session cookie between connection attempts of a
// profile. A tunnel that drops (sleep, a network change) then connects again
// without logging in, and without a new one-time code, while the gateway
// keeps the session. Disconnecting ends it: the client logs out.
type Session struct {
	mu          sync.Mutex
	key, cookie string
}

func (s *Session) get(key string) string {
	if s == nil {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.key != key {
		return ""
	}
	return s.cookie
}

func (s *Session) set(key, cookie string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.key, s.cookie = key, cookie
	s.mu.Unlock()
}

// fortiSessionKey is what a session cookie belongs to.
func fortiSessionKey(cfg *Config) string {
	r := cfg.Res
	return strings.Join([]string{r.Kind, r.Str("server"), r.Str("authGroup"), cfg.Value("auth", "username")}, "\x00")
}

// withFortiSession starts a client with the session of the last connection
// if there is one, and logs in again if the gateway has ended it.
func withFortiSession(ctx context.Context, cfg *Config, start func(cookie string) (Tunnel, error)) (Tunnel, error) {
	key := fortiSessionKey(cfg)
	if cookie := cfg.Session.get(key); cookie != "" {
		cfg.Log.Printf("using the session of the last connection")
		tun, err := start(cookie)
		if err == nil || ctx.Err() != nil {
			return tun, err
		}
		cfg.Session.set("", "")
		cfg.Log.Printf("the gateway did not take that session any more; logging in again")
	}
	cookie, err := fortiLogin(ctx, cfg)
	if err != nil {
		return nil, err
	}
	tun, err := start(cookie)
	if err != nil {
		return nil, err
	}
	cfg.Session.set(key, cookie)
	return tun, nil
}

// fortiAnswer is the gateway's answer to /remote/logincheck.
type fortiAnswer struct {
	status int
	body   string
	kv     map[string]string // ret=…,reqid=…,tokeninfo=…
	cookie string            // SVPNCOOKIE once logged in
}

var (
	fortiKeyRe = regexp.MustCompile(`^[a-z0-9_]+$`)
	fortiErrRe = regexp.MustCompile(`err=([a-z0-9_]+)`) // redir=/remote/login?&err=sslvpn_login_permission_denied
)

// parseFortiRet parses "ret=2,reqid=…,tokeninfo=,chal_msg=…". A part
// without a key belongs to the value before it (a comma in the message).
func parseFortiRet(s string) map[string]string {
	kv := map[string]string{}
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "ret=") {
		return kv
	}
	last := ""
	for _, part := range strings.Split(s, ",") {
		k, v, ok := strings.Cut(part, "=")
		if !ok || !fortiKeyRe.MatchString(k) {
			if last != "" {
				kv[last] += "," + part
			}
			continue
		}
		kv[k], last = v, k
	}
	return kv
}

func fortiPost(ctx context.Context, c *http.Client, base string, form url.Values) (*fortiAnswer, error) {
	req, err := http.NewRequestWithContext(ctx, "POST", base+"/remote/logincheck", strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", fortiUA)
	res, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	a := &fortiAnswer{status: res.StatusCode, body: string(b), kv: parseFortiRet(string(b))}
	for _, ck := range res.Cookies() {
		if ck.Name == "SVPNCOOKIE" && ck.Value != "" {
			a.cookie = ck.Value
		}
	}
	return a, nil
}

// loginError explains an answer without a session or a code request.
func (a *fortiAnswer) loginError() error {
	low := strings.ToLower(a.body)
	reason := ""
	if m := fortiErrRe.FindStringSubmatch(low); m != nil {
		reason = " (" + m[1] + ")"
	}
	switch {
	case strings.Contains(low, "saml"):
		return errors.New("the gateway asks for single sign-on (SAML): turn on Single sign-on in the profile")
	case a.status == http.StatusUnauthorized && strings.Contains(low, "<form"):
		return errors.New("the gateway asks for a one-time code in an old login form opsdeck does not support; use a login with a TOTP secret, or the vendor client")
	case a.kv["ret"] == "0" || reason != "" || a.status == 401 || a.status == 403 || a.status == 405:
		return fmt.Errorf("login failed%s: check the username, password and realm of the profile's login", reason)
	}
	return fmt.Errorf("unexpected answer from the gateway to the login (HTTP %d): %s", a.status, lastN(strings.TrimSpace(a.body), 200))
}

// fortiClient is the HTTP client of a FortiGate login. It does not follow
// redirects (the answers themselves matter), and like openconnect and
// openfortivpn it uses the proxy of the environment, so the gateway sees the
// login come from where the tunnel will.
func fortiClient(pin string) *http.Client {
	c := gatewayClient(pin)
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return c
}

// fortiLogin logs in with the profile's login item, answering a one-time
// code request, and returns the session cookie ("SVPNCOOKIE=…").
func fortiLogin(ctx context.Context, cfg *Config) (string, error) {
	r := cfg.Res
	gw, err := gatewayURL(r.Str("server"))
	if err != nil {
		return "", err
	}
	base := gw.Scheme + "://" + gw.Host
	realm := r.Str("authGroup")
	if realm == "" {
		realm = strings.Trim(gw.Path, "/") // https://vpn.example.com/<realm>
	}
	auth := cfg.Items["auth"]
	user := auth.Get("username")
	c := fortiClient(r.Str("serverCert"))
	cfg.Log.Printf("logging in to %s as %s", gw.Host, user)
	since := time.Now()
	a, err := fortiPost(ctx, c, base, url.Values{"username": {user}, "credential": {auth.Get("password")}, "realm": {realm},
		"ajax": {"1"}, "redir": {"/remote/index"}, "just_logged_in": {"1"}})
	if err != nil {
		return "", certHint(r, gw, err)
	}
	for attempt := 0; ; attempt++ {
		if a.cookie != "" {
			cfg.Log.Printf("logged in")
			return "SVPNCOOKIE=" + a.cookie, nil
		}
		if _, ok := a.kv["tokeninfo"]; !ok {
			if attempt > 0 {
				return "", errors.New("the gateway did not accept the one-time code; connect again for a new one")
			}
			return "", a.loginError()
		}
		if attempt == 3 {
			return "", errors.New("the gateway did not accept the one-time code (3 attempts); connect again for a new one")
		}
		code, err := fortiCode(ctx, cfg, a.kv, attempt, since)
		if err != nil {
			return "", err
		}
		form := url.Values{"username": {user}, "realm": {realm}, "code": {code}, "code2": {""}}
		for _, k := range []string{"reqid", "polid", "grp", "portal", "peer", "magic"} {
			if v, ok := a.kv[k]; ok {
				form.Set(k, v)
			}
		}
		since = time.Now() // a retry only takes codes that arrive after this one
		if a, err = fortiPost(ctx, c, base, form); err != nil {
			return "", err
		}
	}
}

// fortiCode answers the gateway's request for a one-time code: with the
// login's TOTP secret the first time, otherwise from the user.
func fortiCode(ctx context.Context, cfg *Config, kv map[string]string, attempt int, since time.Time) (string, error) {
	msg := strings.TrimSpace(kv["chal_msg"])
	if strings.Contains(msg, "%") {
		if m, err := url.PathUnescape(msg); err == nil {
			msg = m
		}
	}
	if attempt == 0 {
		if secret := cfg.Value("auth", "totp"); secret != "" {
			code, _, _, err := vault.TOTP(secret, time.Now())
			if err == nil {
				cfg.Log.Printf("the gateway asks for a one-time code; sending the login's TOTP code")
				return code, nil
			}
			cfg.Log.Printf("the login's TOTP secret: %v", err)
		}
	}
	if attempt > 0 {
		cfg.Log.Printf("the gateway did not accept the code; asking again")
	} else {
		cfg.Log.Printf("the gateway asks for a one-time code: %s", firstNonEmpty(msg, "(no message)"))
	}
	if cfg.Code == nil {
		return "", fmt.Errorf("the gateway asks for a one-time code (%s), which this connection cannot ask for", firstNonEmpty(msg, "by SMS, email or token"))
	}
	code, err := cfg.Code(ctx, CodePrompt{Network: cfg.Res.Name, Message: msg, Retry: attempt > 0, Since: since})
	if err != nil {
		return "", err
	}
	if code = NormalizeCode(code); code == "" {
		return "", errors.New("no one-time code was entered")
	}
	cfg.Log.Printf("sending the one-time code")
	return code, nil
}

func firstNonEmpty(s ...string) string {
	for _, x := range s {
		if x != "" {
			return x
		}
	}
	return ""
}

// certHint turns an untrusted gateway certificate into the pin to set in the
// profile, in the format of its client (openconnect: pin-sha256 of the key;
// openfortivpn: SHA-256 of the certificate).
func certHint(r *workspace.Resource, gw *url.URL, err error) error {
	var ua x509.UnknownAuthorityError
	var host x509.HostnameError
	var invalid x509.CertificateInvalidError
	if !errors.As(err, &ua) && !errors.As(err, &host) && !errors.As(err, &invalid) && !strings.Contains(err.Error(), "does not match the profile's pin") {
		return err
	}
	addr := gw.Host
	if gw.Port() == "" {
		addr = net.JoinHostPort(gw.Hostname(), "443")
	}
	conn, derr := tls.DialWithDialer(&net.Dialer{Timeout: 10 * time.Second}, "tcp", addr, &tls.Config{InsecureSkipVerify: true, ServerName: gw.Hostname()})
	if derr != nil {
		return err
	}
	defer conn.Close()
	certs := conn.ConnectionState().PeerCertificates
	if len(certs) == 0 {
		return err
	}
	if r.Kind == "OpenFortiVPN" {
		h := sha256.Sum256(certs[0].Raw)
		return fmt.Errorf("the gateway certificate is not trusted. If it is the right gateway, set the profile's server certificate to %s", hex.EncodeToString(h[:]))
	}
	h := sha256.Sum256(certs[0].RawSubjectPublicKeyInfo)
	return fmt.Errorf("the server certificate is not trusted. If it is the right server, set the profile's server certificate to pin-sha256:%s", base64.StdEncoding.EncodeToString(h[:]))
}

// ofvCookieOnStdin reports whether the installed openfortivpn takes a
// session cookie on stdin (newer versions; older ones only log in
// themselves). Cached per binary.
func ofvCookieOnStdin() bool {
	path, err := exec.LookPath("openfortivpn")
	if err != nil {
		return false
	}
	st, err := os.Stat(path)
	if err != nil {
		return false
	}
	key := path + "\x00" + st.ModTime().String()
	ofvHelp.Lock()
	defer ofvHelp.Unlock()
	if v, ok := ofvHelp.m[key]; ok {
		return v
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, _ := sysproc.CommandContext(ctx, path, "--help").CombinedOutput()
	ok := strings.Contains(string(out), "--cookie-on-stdin")
	ofvHelp.m[key] = ok
	return ok
}

var ofvHelp = struct {
	sync.Mutex
	m map[string]bool
}{m: map[string]bool{}}
