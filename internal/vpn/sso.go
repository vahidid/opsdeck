package vpn

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/hkdf"
)

// Single sign-on (SAML) for VPN gateways, without copying cookies by hand.
//
// The identity provider's login page is shown either in a login window
// opsdeck drives (Chrome/Edge/Chromium through the DevTools protocol, with a
// browser profile per VPN profile so the IdP session is remembered) or in
// the user's default browser where the gateway calls back to localhost.
// Once the login completes opsdeck has a VPN session cookie and starts the
// client with it:
//
//   - Cisco AnyConnect: opsdeck posts the aggregate-auth XML itself. With
//     external-browser SSO the gateway sends the token, HPKE-encrypted to
//     opsdeck's STRAP key, to http://localhost:29786; with embedded-browser
//     SSO opsdeck reads the token cookie in its login window. The token is
//     traded for the session cookie, which openconnect then uses (with the
//     STRAP key, in the same format as openconnect --authenticate).
//   - GlobalProtect SAML: prelogin, then the prelogin/portal cookie from the
//     final page's headers (or the globalprotectcallback: URL).
//   - FortiGate SAML: /remote/saml/start?redirect=1 with a callback on
//     127.0.0.1:8020 (FortiOS 7+), or the SVPNCOOKIE set in the login window.

// ssoResult is what the VPN client needs after a successful SSO login.
type ssoResult struct {
	Cookie     string // session cookie (or GlobalProtect cookie used as password)
	User       string // GlobalProtect: SAML user name
	UserGroup  string // GlobalProtect: portal:portal-userauthcookie, gateway:prelogin-cookie…
	ServerCert string // AnyConnect: certificate fingerprint reported by the gateway
	ConnectURL string // AnyConnect: URL to connect to after authentication
}

// Fixed callback ports of the vendors' clients: one login at a time each.
var (
	ssoMuAnyConnect sync.Mutex // localhost:29786
	ssoMuFortinet   sync.Mutex // 127.0.0.1:8020
)

// ---------------------------------------------------------------------------
// Login UI

type loginUI struct {
	cfg     *Config
	mode    string // window | system
	exe     string
	browser *Browser
}

// newLoginUI picks how to show the login: ssoBrowser = auto (window when a
// Chromium-family browser exists), window, or system.
func newLoginUI(cfg *Config) (*loginUI, error) {
	u := &loginUI{cfg: cfg, exe: FindBrowser(cfg.Browser)}
	switch m := cfg.Res.Str("ssoBrowser"); m {
	case "", "auto":
		u.mode = "window"
		if u.exe == "" {
			u.mode = "system"
		}
	case "window", "embedded":
		if u.exe == "" {
			return nil, errors.New("single sign-on is set to use a login window, but no Chrome, Edge, Chromium or Brave was found (set its path in App settings → VPN)")
		}
		u.mode = "window"
	case "system":
		u.mode = "system"
	default:
		return nil, fmt.Errorf("unknown ssoBrowser %q (auto, window or system)", m)
	}
	return u, nil
}

// needWindow fails when a flow can only complete in opsdeck's login window.
func (u *loginUI) needWindow(why string) error {
	if u.mode == "window" {
		return nil
	}
	if u.exe == "" {
		return fmt.Errorf("%s; install Chrome, Edge, Chromium or Brave (or set its path in App settings → VPN) so opsdeck can open a login window", why)
	}
	return fmt.Errorf("%s; set the profile's SSO browser to \"auto\" or \"login window\"", why)
}

func (u *loginUI) ensureWindow() error {
	if u.browser != nil {
		return nil
	}
	var pins []string
	if p := u.cfg.Res.Str("serverCert"); strings.HasPrefix(p, "pin-sha256:") {
		pins = append(pins, strings.TrimPrefix(p, "pin-sha256:"))
	}
	b, err := LaunchBrowser(u.exe, filepath.Join(u.cfg.StateDir, "browser"), pins)
	if err != nil {
		return err
	}
	u.browser = b
	u.cfg.Log.Printf("opened a login window (%s)", filepath.Base(u.exe))
	return nil
}

// open shows a login page.
func (u *loginUI) open(loginURL string) error {
	if u.mode == "system" {
		u.cfg.Log.Printf("single sign-on: open %s", loginURL)
		u.cfg.prompt(AuthPrompt{URL: loginURL})
		if err := openURL(loginURL); err != nil {
			u.cfg.Log.Printf("could not open the default browser (%v): use the link on the VPN page", err)
		}
		return nil
	}
	if err := u.ensureWindow(); err != nil {
		return err
	}
	u.cfg.Log.Printf("single sign-on: complete the login in the login window")
	u.cfg.prompt(AuthPrompt{Window: true})
	return u.browser.Navigate(loginURL)
}

// showHTML shows a document in the login window (SAML POST bindings).
func (u *loginUI) showHTML(html string) error {
	if err := u.ensureWindow(); err != nil {
		return err
	}
	u.cfg.prompt(AuthPrompt{Window: true})
	return u.browser.SetContent(html)
}

// closed reports a login window the user closed.
func (u *loginUI) closed() <-chan struct{} {
	if u.browser == nil {
		return nil
	}
	return u.browser.Done()
}

func (u *loginUI) close() {
	if u.browser != nil {
		u.browser.Close()
		u.browser = nil
	}
}

// ---------------------------------------------------------------------------
// HTTP client for gateway authentication

// pinMatches checks a certificate against a pin: pin-sha256:<base64 SPKI
// hash>, or the hex SHA-256/SHA-1 of the certificate (sha256:/sha1: prefix
// optional).
func pinMatches(pin string, cert *x509.Certificate) bool {
	pin = strings.TrimSpace(pin)
	if b64, ok := strings.CutPrefix(pin, "pin-sha256:"); ok {
		h := sha256.Sum256(cert.RawSubjectPublicKeyInfo)
		return base64.StdEncoding.EncodeToString(h[:]) == b64
	}
	hexPin := strings.ToLower(strings.NewReplacer(":", "", "sha256", "", "sha1", "").Replace(pin))
	s256 := sha256.Sum256(cert.Raw)
	s1 := sha1.Sum(cert.Raw)
	return hexPin == hex.EncodeToString(s256[:]) || hexPin == hex.EncodeToString(s1[:])
}

func gatewayClient(pin string) *http.Client {
	jar, _ := cookiejar.New(nil)
	tr := http.DefaultTransport.(*http.Transport).Clone()
	if pin != "" {
		tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true, VerifyPeerCertificate: func(raw [][]byte, _ [][]*x509.Certificate) error {
			if len(raw) == 0 {
				return errors.New("no certificate")
			}
			cert, err := x509.ParseCertificate(raw[0])
			if err != nil {
				return err
			}
			if !pinMatches(pin, cert) {
				return fmt.Errorf("gateway certificate does not match the profile's pin")
			}
			return nil
		}}
	}
	return &http.Client{Jar: jar, Transport: tr, Timeout: 60 * time.Second}
}

// gatewayURL turns "vpn.example.com/group" into https://vpn.example.com/group.
func gatewayURL(server string) (*url.URL, error) {
	s := strings.TrimSpace(server)
	if !strings.Contains(s, "://") {
		s = "https://" + s
	}
	u, err := url.Parse(s)
	if err != nil || u.Host == "" {
		return nil, fmt.Errorf("invalid server %q", server)
	}
	return u, nil
}

func waitTick(ctx context.Context, ui *loginUI) error {
	select {
	case <-ctx.Done():
		return fmt.Errorf("single sign-on did not complete: %w", ctx.Err())
	case <-ui.closed():
		return errors.New("the login window was closed before the sign-in finished")
	case <-time.After(300 * time.Millisecond):
		return nil
	}
}

// ---------------------------------------------------------------------------
// Cisco AnyConnect

const anyConnectUA = "AnyConnect Linux_64 4.10.07061"

type acResponse struct {
	XMLName xml.Name `xml:"config-auth"`
	Type    string   `xml:"type,attr"`
	Auth    struct {
		ID          string `xml:"id,attr"`
		Message     string `xml:"message"`
		Error       string `xml:"error"`
		Login       string `xml:"sso-v2-login"`
		LoginFinal  string `xml:"sso-v2-login-final"`
		TokenCookie string `xml:"sso-v2-token-cookie-name"`
		ErrorCookie string `xml:"sso-v2-error-cookie-name"`
		BrowserMode string `xml:"sso-v2-browser-mode"`
	} `xml:"auth"`
	Opaque struct {
		Inner []byte `xml:",innerxml"`
		IsFor string `xml:"is-for,attr"`
	} `xml:"opaque"`
	SessionToken string `xml:"session-token"`
	CertHash     string `xml:"config>vpn-base-config>server-cert-hash"`
}

func acPost(ctx context.Context, c *http.Client, target string, body string, keys *strapKeys) (*acResponse, error) {
	req, _ := http.NewRequestWithContext(ctx, "POST", target, strings.NewReader(body))
	keys.headers(req.Header)
	req.Header.Set("User-Agent", anyConnectUA)
	req.Header.Set("Accept", "*/*")
	req.Header.Set("X-Transcend-Version", "1")
	req.Header.Set("X-Aggregate-Auth", "1")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	res, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	var r acResponse
	if err := xml.Unmarshal(b, &r); err != nil {
		return nil, fmt.Errorf("the gateway did not answer with AnyConnect XML (HTTP %d): %v", res.StatusCode, err)
	}
	return &r, nil
}

func xmlEsc(s string) string {
	var b bytes.Buffer
	xml.EscapeText(&b, []byte(s))
	return b.String()
}

func ssoAnyConnect(ctx context.Context, cfg *Config, ui *loginUI) (*ssoResult, error) {
	gw, err := gatewayURL(cfg.Res.Str("server"))
	if err != nil {
		return nil, err
	}
	c := gatewayClient(cfg.Res.Str("serverCert"))
	group := ""
	if g := cfg.Res.Str("authGroup"); g != "" {
		group = "<group-select>" + xmlEsc(g) + "</group-select>"
	}
	initReq := `<?xml version="1.0" encoding="UTF-8"?>
<config-auth client="vpn" type="init" aggregate-auth-version="2"><version who="vpn">4.10.07061</version><device-id>linux-64</device-id>` +
		group + `<group-access>` + xmlEsc(gw.String()) + `</group-access><capabilities><auth-method>single-sign-on-v2</auth-method><auth-method>single-sign-on-external-browser</auth-method></capabilities></config-auth>`
	keys, err := newStrapKeys()
	if err != nil {
		return nil, err
	}
	r, err := acPost(ctx, c, gw.String(), initReq, keys)
	if err != nil {
		return nil, err
	}
	if r.Auth.Login == "" {
		msg := strings.TrimSpace(r.Auth.Error + " " + r.Auth.Message)
		return nil, fmt.Errorf("the gateway did not offer single sign-on for this connection profile (%s); check the group or turn SSO off", strings.TrimSpace(msg))
	}
	var token string
	if strings.EqualFold(r.Auth.BrowserMode, "external") {
		cfg.Log.Printf("gateway uses external-browser SSO")
		if token, err = acExternalToken(ctx, cfg, ui, r.Auth.Login, keys); err != nil {
			return nil, err
		}
	} else {
		cfg.Log.Printf("gateway uses embedded-browser SSO")
		if token, err = acEmbeddedToken(ctx, ui, r); err != nil {
			return nil, err
		}
	}
	cfg.Log.Printf("signed in; requesting the VPN session")
	opaque := ""
	if len(r.Opaque.Inner) > 0 {
		opaque = `<opaque is-for="` + xmlEsc(r.Opaque.IsFor) + `">` + string(r.Opaque.Inner) + `</opaque>`
	}
	reply := `<?xml version="1.0" encoding="UTF-8"?>
<config-auth client="vpn" type="auth-reply" aggregate-auth-version="2"><version who="vpn">4.10.07061</version><device-id>linux-64</device-id><session-token/><session-id/>` +
		opaque + `<auth><sso-token>` + xmlEsc(token) + `</sso-token></auth></config-auth>`
	done, err := acPost(ctx, c, gw.String(), reply, keys)
	if err != nil {
		return nil, err
	}
	if done.SessionToken == "" {
		return nil, fmt.Errorf("the gateway did not accept the SSO login: %s", strings.TrimSpace(done.Auth.Error+" "+done.Auth.Message))
	}
	// openconnect signs the tunnel request with the same STRAP key when the
	// cookie carries it (the format of openconnect --authenticate).
	cookie := "openconnect_strapkey=" + keys.privB64() + "; webvpn=" + done.SessionToken
	return &ssoResult{Cookie: cookie, ConnectURL: gw.String(), ServerCert: done.CertHash}, nil
}

// acEmbeddedToken reads the SSO token cookie in the login window once the
// browser reaches the gateway's final URL.
func acEmbeddedToken(ctx context.Context, ui *loginUI, r *acResponse) (string, error) {
	if err := ui.needWindow("this AnyConnect gateway uses embedded-browser SSO, where opsdeck reads the login token from its own login window"); err != nil {
		return "", err
	}
	if err := ui.open(r.Auth.Login); err != nil {
		return "", err
	}
	final := strings.SplitN(r.Auth.LoginFinal, "?", 2)[0]
	for {
		if err := waitTick(ctx, ui); err != nil {
			return "", err
		}
		cur := ui.browser.URL()
		if final != "" && !strings.HasPrefix(cur, final) {
			continue
		}
		if r.Auth.ErrorCookie != "" {
			if e := ui.browser.Cookie(r.Auth.LoginFinal, r.Auth.ErrorCookie); e != "" {
				return "", fmt.Errorf("the identity provider reported an error: %s", e)
			}
		}
		if token := ui.browser.Cookie(r.Auth.LoginFinal, r.Auth.TokenCookie); token != "" {
			return token, nil
		}
	}
}

// strapKeys are the client keys of Cisco's STRAP: one signs the tunnel
// request, the other receives the HPKE-encrypted external-browser token.
type strapKeys struct {
	sign *ecdsa.PrivateKey
	dh   *ecdh.PrivateKey
}

func newStrapKeys() (*strapKeys, error) {
	sign, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	dh, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	return &strapKeys{sign: sign, dh: dh}, nil
}

func (k *strapKeys) headers(h http.Header) {
	if k == nil {
		return
	}
	sp, _ := x509.MarshalPKIXPublicKey(&k.sign.PublicKey)
	dp, _ := x509.MarshalPKIXPublicKey(k.dh.PublicKey())
	h.Set("X-AnyConnect-STRAP-Pubkey", base64.StdEncoding.EncodeToString(sp))
	h.Set("X-AnyConnect-STRAP-DH-Pubkey", base64.StdEncoding.EncodeToString(dp))
}

func (k *strapKeys) privB64() string {
	der, _ := x509.MarshalECPrivateKey(k.sign)
	return base64.StdEncoding.EncodeToString(der)
}

// open decrypts the token a gateway sends to localhost:29786: TLVs after a
// 0x0001 marker (1 server DH key, 2 GCM tag, 3 ciphertext, 4 IV), key =
// HKDF-SHA256(ECDH secret, info "AC_ECIES"), AES-256-GCM with a 12-byte tag.
func (k *strapKeys) open(blob []byte) (string, error) {
	if len(blob) < 2 || binary.BigEndian.Uint16(blob) != 1 {
		return "", errors.New("bad token format")
	}
	tags := map[uint16][]byte{}
	for pos := 2; pos < len(blob); {
		if pos+4 > len(blob) {
			return "", errors.New("truncated token")
		}
		tag, n := binary.BigEndian.Uint16(blob[pos:]), int(binary.BigEndian.Uint16(blob[pos+2:]))
		if pos+4+n > len(blob) || tag < 1 || tag > 4 || tags[tag] != nil {
			return "", errors.New("bad token TLV")
		}
		tags[tag] = blob[pos+4 : pos+4+n]
		pos += 4 + n
	}
	if tags[1] == nil || tags[3] == nil || len(tags[2]) != 12 || len(tags[4]) != 12 {
		return "", errors.New("incomplete token")
	}
	pk, err := x509.ParsePKIXPublicKey(tags[1])
	if err != nil {
		return "", err
	}
	ec, ok := pk.(*ecdsa.PublicKey)
	if !ok {
		return "", errors.New("unexpected server key type")
	}
	pub, err := ec.ECDH()
	if err != nil {
		return "", err
	}
	secret, err := k.dh.ECDH(pub)
	if err != nil {
		return "", err
	}
	key := make([]byte, 32)
	if _, err := io.ReadFull(hkdf.New(sha256.New, secret, nil, []byte("AC_ECIES")), key); err != nil {
		return "", err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCMWithTagSize(block, 12)
	if err != nil {
		return "", err
	}
	plain, err := gcm.Open(nil, tags[4], append(append([]byte{}, tags[3]...), tags[2]...), nil)
	if err != nil {
		return "", fmt.Errorf("token decryption failed: %w", err)
	}
	for _, c := range plain {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z') {
			return "", errors.New("token is not alphanumeric")
		}
	}
	return string(plain), nil
}

// acExternalToken runs Cisco's external-browser SSO: the gateway sends the
// browser to http://localhost:29786/api/sso/<encrypted token>.
func acExternalToken(ctx context.Context, cfg *Config, ui *loginUI, loginURL string, keys *strapKeys) (string, error) {
	ssoMuAnyConnect.Lock()
	defer ssoMuAnyConnect.Unlock()
	type result struct {
		token string
		err   error
	}
	results := make(chan result, 1)
	mux := http.NewServeMux()
	mux.HandleFunc("/api/sso/", func(w http.ResponseWriter, r *http.Request) {
		raw := strings.TrimPrefix(r.URL.EscapedPath(), "/api/sso/")
		dec, _ := url.PathUnescape(raw)
		blob, err := base64.StdEncoding.DecodeString(dec)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		tok, err := keys.open(blob)
		select {
		case results <- result{tok, err}:
		default:
		}
		if ret := r.URL.Query().Get("return"); ret != "" && err == nil {
			http.Redirect(w, r, ret, http.StatusFound)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, `<html><body style="font-family:sans-serif;padding:2em">Signed in. You can close this window.</body></html>`)
	})
	srv := &http.Server{Handler: mux}
	listening := 0
	for _, addr := range []string{"127.0.0.1:29786", "[::1]:29786"} {
		if ln, err := net.Listen("tcp", addr); err == nil {
			listening++
			go srv.Serve(ln)
		}
	}
	defer srv.Close()
	if listening == 0 {
		return "", errors.New("cannot listen on localhost:29786 for the AnyConnect SSO callback (is another VPN client signing in?)")
	}
	if err := ui.open(loginURL); err != nil {
		return "", err
	}
	for {
		select {
		case r := <-results:
			return r.token, r.err
		default:
		}
		if err := waitTick(ctx, ui); err != nil {
			return "", err
		}
	}
}

// ---------------------------------------------------------------------------
// GlobalProtect

type gpPrelogin struct {
	Status   string `xml:"status"`
	Method   string `xml:"saml-auth-method"`
	Request  string `xml:"saml-request"`
	Message  string `xml:"msg"`
	Region   string `xml:"region"`
	AuthType string `xml:"authentication-message"`
}

func ssoGlobalProtect(ctx context.Context, cfg *Config, ui *loginUI) (*ssoResult, error) {
	gw, err := gatewayURL(cfg.Res.Str("server"))
	if err != nil {
		return nil, err
	}
	if err := ui.needWindow("GlobalProtect SSO returns its login cookie only to the browser that signed in"); err != nil {
		return nil, err
	}
	c := gatewayClient(cfg.Res.Str("serverCert"))
	var pre gpPrelogin
	iface := ""
	for _, ep := range []struct{ iface, path string }{{"portal", "/global-protect/prelogin.esp"}, {"gateway", "/ssl-vpn/prelogin.esp"}} {
		form := "tmp=tmp&kerberos-support=yes&ipv6-support=yes&clientVer=4100&clientos=Linux"
		req, _ := http.NewRequestWithContext(ctx, "POST", gw.Scheme+"://"+gw.Host+ep.path+"?"+form, strings.NewReader(form))
		req.Header.Set("User-Agent", "PAN GlobalProtect")
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		res, err := c.Do(req)
		if err != nil {
			return nil, err
		}
		b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
		res.Body.Close()
		var p gpPrelogin
		if xml.Unmarshal(b, &p) == nil && strings.EqualFold(p.Status, "Success") && p.Request != "" {
			pre, iface = p, ep.iface
			break
		}
	}
	if iface == "" {
		return nil, errors.New("the GlobalProtect portal/gateway did not ask for SAML login; turn SSO off for this profile")
	}
	samlReq, err := base64.StdEncoding.DecodeString(pre.Request)
	if err != nil {
		return nil, fmt.Errorf("invalid SAML request from the %s: %v", iface, err)
	}
	var mu sync.Mutex
	got := map[string]string{}
	take := func(k, v string) {
		k = strings.ToLower(k)
		switch k {
		case "saml-username", "prelogin-cookie", "portal-userauthcookie", "saml-auth-status":
			if v != "" {
				mu.Lock()
				got[k] = v
				mu.Unlock()
			}
		}
	}
	if err := ui.ensureWindow(); err != nil {
		return nil, err
	}
	ui.browser.On(func(method string, params json.RawMessage) {
		switch method {
		case "Network.responseReceived":
			var p struct {
				Response struct {
					Headers map[string]string `json:"headers"`
				} `json:"response"`
			}
			if json.Unmarshal(params, &p) == nil {
				for k, v := range p.Response.Headers {
					take(k, v)
				}
			}
		case "Network.requestWillBeSent", "Page.frameRequestedNavigation":
			// GlobalProtect 6+: the final page hands the result to the client
			// as globalprotectcallback:<base64 XML>
			var p struct {
				URL     string `json:"url"`
				Request struct {
					URL string `json:"url"`
				} `json:"request"`
			}
			json.Unmarshal(params, &p)
			u := p.URL + p.Request.URL
			if data, ok := strings.CutPrefix(u, "globalprotectcallback:"); ok {
				if b, err := base64.StdEncoding.DecodeString(strings.TrimSpace(data)); err == nil {
					gpTakeTags(string(b), take)
				}
			}
		}
	})
	if strings.EqualFold(pre.Method, "POST") {
		err = ui.showHTML(string(samlReq))
	} else {
		err = ui.open(string(samlReq))
	}
	if err != nil {
		return nil, err
	}
	for {
		if err := waitTick(ctx, ui); err != nil {
			return nil, err
		}
		gpTakeTags(ui.browser.HTML(), take) // some gateways put the result in HTML comments
		mu.Lock()
		user, pre, portal, status := got["saml-username"], got["prelogin-cookie"], got["portal-userauthcookie"], got["saml-auth-status"]
		mu.Unlock()
		if status == "-1" {
			return nil, errors.New("the GlobalProtect gateway reported a failed SAML login")
		}
		if user == "" || (pre == "" && portal == "") {
			continue
		}
		res := &ssoResult{User: user}
		if portal != "" && portal != "empty" {
			res.Cookie, res.UserGroup = portal, iface+":portal-userauthcookie"
		} else {
			res.Cookie, res.UserGroup = pre, iface+":prelogin-cookie"
		}
		return res, nil
	}
}

var gpTagRe = regexp.MustCompile(`<(saml-username|prelogin-cookie|portal-userauthcookie|saml-auth-status)>([^<]*)</`)

func gpTakeTags(s string, take func(k, v string)) {
	for _, m := range gpTagRe.FindAllStringSubmatch(s, -1) {
		take(m[1], strings.TrimSpace(m[2]))
	}
}

// ---------------------------------------------------------------------------
// FortiGate

func ssoFortinet(ctx context.Context, cfg *Config, ui *loginUI) (*ssoResult, error) {
	gw, err := gatewayURL(cfg.Res.Str("server"))
	if err != nil {
		return nil, err
	}
	ssoMuFortinet.Lock()
	defer ssoMuFortinet.Unlock()
	c := gatewayClient(cfg.Res.Str("serverCert"))
	base := gw.Scheme + "://" + gw.Host
	ids := make(chan string, 1)
	// FortiOS 7+: after the login the browser is redirected to
	// http://127.0.0.1:8020/?id=<id>, which opsdeck trades for SVPNCOOKIE.
	ln, lerr := net.Listen("tcp", "127.0.0.1:8020")
	if lerr == nil {
		srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if id := r.URL.Query().Get("id"); id != "" {
				select {
				case ids <- id:
				default:
				}
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				fmt.Fprint(w, `<html><body style="font-family:sans-serif;padding:2em">Signed in. You can close this window.</body></html>`)
				return
			}
			http.NotFound(w, r)
		})}
		go srv.Serve(ln)
		defer srv.Close()
	} else {
		cfg.Log.Printf("port 8020 is busy (%v); waiting for the cookie in the login window instead", lerr)
		if err := ui.needWindow("the FortiGate login callback port 127.0.0.1:8020 is in use"); err != nil {
			return nil, err
		}
	}
	start := base + "/remote/saml/start?redirect=1"
	if realm := cfg.Res.Str("authGroup"); realm != "" {
		start += "&realm=" + url.QueryEscape(realm)
	}
	if err := ui.open(start); err != nil {
		return nil, err
	}
	for {
		select {
		case id := <-ids:
			cfg.Log.Printf("signed in; requesting the VPN session")
			req, _ := http.NewRequestWithContext(ctx, "GET", base+"/remote/saml/auth_id?id="+url.QueryEscape(id), nil)
			res, err := c.Do(req)
			if err != nil {
				return nil, err
			}
			io.Copy(io.Discard, res.Body)
			res.Body.Close()
			for _, ck := range c.Jar.Cookies(gw) {
				if ck.Name == "SVPNCOOKIE" && ck.Value != "" {
					return &ssoResult{Cookie: "SVPNCOOKIE=" + ck.Value}, nil
				}
			}
			return nil, fmt.Errorf("the FortiGate did not return a session cookie for the SSO login (HTTP %d)", res.StatusCode)
		default:
		}
		if err := waitTick(ctx, ui); err != nil {
			return nil, err
		}
		if ui.browser != nil {
			if v := ui.browser.Cookie(base+"/", "SVPNCOOKIE"); v != "" {
				return &ssoResult{Cookie: "SVPNCOOKIE=" + v}, nil
			}
		}
	}
}

// ssoLogin runs the SSO flow of an OpenConnect protocol.
func ssoLogin(ctx context.Context, cfg *Config, protocol string) (*ssoResult, error) {
	ui, err := newLoginUI(cfg)
	if err != nil {
		return nil, err
	}
	defer ui.close()
	cfg.Log.Printf("single sign-on (%s, %s)", protocol, map[string]string{"window": "login window", "system": "default browser"}[ui.mode])
	switch protocol {
	case "anyconnect":
		return ssoAnyConnect(ctx, cfg, ui)
	case "gp":
		return ssoGlobalProtect(ctx, cfg, ui)
	case "fortinet":
		return ssoFortinet(ctx, cfg, ui)
	}
	return nil, fmt.Errorf("single sign-on is supported for AnyConnect/ocserv, GlobalProtect and FortiGate, not %q", protocol)
}
