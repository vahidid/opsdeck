package vpn

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/hkdf"

	"opsdeck/internal/workspace"
)

// The fake identity provider "signs in" right away, like a user whose IdP
// session is remembered.
const autoSubmit = `<form method="post" action="%s"><input name="SAMLResponse" value="ok"></form><script>document.forms[0].submit()</script>`

func spkiPin(srv *httptest.Server) string {
	h := sha256.Sum256(srv.Certificate().RawSubjectPublicKeyInfo)
	return "pin-sha256:" + base64.StdEncoding.EncodeToString(h[:])
}

func ssoCfg(t *testing.T, spec map[string]any) (*Config, *Log) {
	log := NewLog()
	dir := t.TempDir()
	os.MkdirAll(dir+"/run", 0o700)
	return &Config{Res: &workspace.Resource{Kind: "OpenConnect", Name: "sso", Spec: spec}, StateDir: dir, RunDir: dir + "/run", Log: log}, log
}

func noProxy(t *testing.T) {
	for _, k := range []string{"https_proxy", "HTTPS_PROXY", "http_proxy", "HTTP_PROXY", "all_proxy", "ALL_PROXY"} {
		t.Setenv(k, "")
		os.Unsetenv(k)
	}
}

// fakeASA emulates a Cisco ASA's aggregate-auth SSO in embedded or external
// (STRAP/HPKE) browser mode.
type fakeASA struct {
	*httptest.Server
	external   bool
	mu         sync.Mutex
	dhPub      string // client's X-AnyConnect-STRAP-DH-Pubkey
	gotToken   string
	tunnelAuth string // Cookie of the client's CONNECT /CSTP request
}

const ssoToken = "Tok3nFromTheIdP"

func newFakeASA(t *testing.T, external bool) *fakeASA {
	f := &fakeASA{external: external}
	f.Server = httptest.NewTLSServer(http.HandlerFunc(f.handle))
	t.Cleanup(f.Close)
	return f
}

func (f *fakeASA) handle(w http.ResponseWriter, r *http.Request) {
	if r.Method == "CONNECT" { // the tunnel request: record how the client authenticates
		f.mu.Lock()
		f.tunnelAuth = r.Header.Get("Cookie")
		f.mu.Unlock()
		http.Error(w, "this test gateway has no tunnel", http.StatusUnauthorized)
		return
	}
	if k := r.Header.Get("X-AnyConnect-STRAP-DH-Pubkey"); k != "" {
		f.mu.Lock()
		f.dhPub = k
		f.mu.Unlock()
	}
	switch r.URL.Path {
	case "/":
		b, _ := io.ReadAll(r.Body)
		body := string(b)
		w.Header().Set("Content-Type", "text/xml")
		switch {
		case strings.Contains(body, `type="init"`):
			mode := ""
			if f.external {
				mode = "<sso-v2-browser-mode>external</sso-v2-browser-mode>"
			}
			fmt.Fprintf(w, `<?xml version="1.0" encoding="UTF-8"?>
<config-auth client="vpn" type="auth-request" aggregate-auth-version="2">
<opaque is-for="sg"><tunnel-group>LAB-SSO</tunnel-group><auth-handle>42</auth-handle><config-hash>1</config-hash></opaque>
<auth id="main"><title>Login</title><message>Please complete the authentication process in the AnyConnect Login window.</message><banner></banner>
<sso-v2-login>%[1]s/saml/login</sso-v2-login><sso-v2-login-final>%[1]s/+CSCOE+/saml_ac_login.html</sso-v2-login-final>
<sso-v2-token-cookie-name>acSamlv2Token</sso-v2-token-cookie-name><sso-v2-error-cookie-name>acSamlv2Error</sso-v2-error-cookie-name>%[2]s
<form><input type="sso" name="sso-token"></input></form></auth></config-auth>`, f.URL, mode)
		case strings.Contains(body, `type="auth-reply"`):
			if !strings.Contains(body, "<auth-handle>42</auth-handle>") {
				http.Error(w, "opaque data missing", 400)
				return
			}
			tok := between(body, "<sso-token>", "</sso-token>")
			f.mu.Lock()
			f.gotToken = tok
			f.mu.Unlock()
			if tok != ssoToken {
				fmt.Fprint(w, `<?xml version="1.0"?><config-auth client="vpn" type="auth-request"><auth id="main"><error>bad token</error></auth></config-auth>`)
				return
			}
			fmt.Fprint(w, `<?xml version="1.0" encoding="UTF-8"?>
<config-auth client="vpn" type="complete" aggregate-auth-version="2"><session-id>7</session-id><session-token>SESSION-0K-123</session-token>
<auth id="success"><message id="0" param1="" param2=""></message></auth>
<config client="vpn" type="private"><vpn-base-config><server-cert-hash>AABBCCDDEEFF00112233445566778899AABBCCDD</server-cert-hash></vpn-base-config><opaque is-for="vpn-client"></opaque></config></config-auth>`)
		}
	case "/saml/login":
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprintf(w, autoSubmit, "/saml/acs")
	case "/saml/acs":
		if f.external {
			f.mu.Lock()
			pub := f.dhPub
			f.mu.Unlock()
			blob, err := hpkeSeal(pub, ssoToken)
			if err != nil {
				http.Error(w, err.Error(), 500)
				return
			}
			http.Redirect(w, r, "http://localhost:29786/api/sso/"+url.PathEscape(blob)+"?return="+url.QueryEscape(f.URL+"/done"), http.StatusFound)
			return
		}
		http.SetCookie(w, &http.Cookie{Name: "acSamlv2Token", Value: ssoToken, Path: "/"})
		http.Redirect(w, r, "/+CSCOE+/saml_ac_login.html", http.StatusFound)
	default:
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, "<html>ok</html>")
	}
}

func between(s, a, b string) string {
	i := strings.Index(s, a)
	if i < 0 {
		return ""
	}
	s = s[i+len(a):]
	if j := strings.Index(s, b); j >= 0 {
		return s[:j]
	}
	return ""
}

// hpkeSeal encrypts the SSO token the way a Cisco gateway does for
// external-browser SSO: ECDH P-256 with the client's STRAP DH key,
// HKDF-SHA256 (info "AC_ECIES"), AES-256-GCM with a 12-byte tag, in TLVs.
func hpkeSeal(clientPubB64, token string) (string, error) {
	der, err := base64.StdEncoding.DecodeString(clientPubB64)
	if err != nil {
		return "", err
	}
	pk, err := x509.ParsePKIXPublicKey(der)
	if err != nil {
		return "", err
	}
	clientPub, err := pk.(*ecdsa.PublicKey).ECDH()
	if err != nil {
		return "", err
	}
	eph, _ := ecdh.P256().GenerateKey(rand.Reader)
	secret, err := eph.ECDH(clientPub)
	if err != nil {
		return "", err
	}
	key := make([]byte, 32)
	io.ReadFull(hkdf.New(sha256.New, secret, nil, []byte("AC_ECIES")), key)
	block, _ := aes.NewCipher(key)
	gcm, _ := cipher.NewGCMWithTagSize(block, 12)
	iv := make([]byte, 12)
	rand.Read(iv)
	sealed := gcm.Seal(nil, iv, []byte(token), nil)
	ct, tag := sealed[:len(token)], sealed[len(token):]
	ephDER, err := x509.MarshalPKIXPublicKey(eph.PublicKey())
	if err != nil {
		return "", err
	}
	var b []byte
	b = binary.BigEndian.AppendUint16(b, 1)
	for _, tlv := range []struct {
		tag  uint16
		data []byte
	}{{1, ephDER}, {2, tag}, {3, ct}, {4, iv}} {
		b = binary.BigEndian.AppendUint16(b, tlv.tag)
		b = binary.BigEndian.AppendUint16(b, uint16(len(tlv.data)))
		b = append(b, tlv.data...)
	}
	return base64.StdEncoding.EncodeToString(b), nil
}

func newJar() http.CookieJar {
	j, _ := cookiejar.New(nil)
	return j
}

func TestSSOAnyConnectEmbedded(t *testing.T) {
	exe := testBrowser(t)
	noProxy(t)
	asa := newFakeASA(t, false)
	cfg, log := ssoCfg(t, map[string]any{"server": asa.URL, "serverCert": spkiPin(asa.Server), "ssoBrowser": "window"})
	cfg.Browser = exe
	var prompts []AuthPrompt
	cfg.Auth = func(p AuthPrompt) { prompts = append(prompts, p) }
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	res, err := ssoLogin(ctx, cfg, "anyconnect")
	if err != nil {
		t.Fatalf("%v\n%s", err, log.Tail(20))
	}
	if !strings.HasSuffix(res.Cookie, "; webvpn=SESSION-0K-123") || res.ServerCert == "" {
		t.Fatalf("result: %+v", res)
	}
	if len(prompts) == 0 || !prompts[0].Window {
		t.Fatalf("expected a login-window prompt, got %+v", prompts)
	}
}

func TestSSOAnyConnectExternalBrowser(t *testing.T) {
	noProxy(t)
	asa := newFakeASA(t, true)
	// the user's default browser, simulated: follow the login link
	run := func(t *testing.T, cfg *Config, log *Log) {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		res, err := ssoLogin(ctx, cfg, "anyconnect")
		if err != nil {
			t.Fatalf("%v\n%s", err, log.Tail(30))
		}
		if !strings.HasSuffix(res.Cookie, "; webvpn=SESSION-0K-123") || !strings.HasPrefix(res.Cookie, "openconnect_strapkey=") || res.ConnectURL == "" {
			t.Fatalf("result: %+v\n%s", res, log.Tail(30))
		}
		asa.mu.Lock()
		got := asa.gotToken
		asa.mu.Unlock()
		if got != ssoToken {
			t.Fatalf("gateway got token %q", got)
		}
	}
	t.Run("system-browser", func(t *testing.T) {
		cfg, log := ssoCfg(t, map[string]any{"server": asa.URL, "serverCert": spkiPin(asa.Server), "ssoBrowser": "system"})
		cfg.Auth = func(p AuthPrompt) {
			if p.URL == "" {
				return
			}
			go func() {
				c := &http.Client{Transport: &http.Transport{TLSClientConfig: asa.Client().Transport.(*http.Transport).TLSClientConfig}}
				jar := newJar()
				c.Jar = jar
				// the IdP auto-submits with JavaScript; a plain client posts the form itself
				if res, err := c.Get(p.URL); err == nil {
					res.Body.Close()
				}
				if res, err := c.PostForm(asa.URL+"/saml/acs", url.Values{"SAMLResponse": {"ok"}}); err == nil {
					res.Body.Close()
				}
			}()
		}
		run(t, cfg, log)
	})
	t.Run("login-window", func(t *testing.T) {
		exe := testBrowser(t)
		cfg, log := ssoCfg(t, map[string]any{"server": asa.URL, "serverCert": spkiPin(asa.Server), "ssoBrowser": "window"})
		cfg.Browser = exe
		run(t, cfg, log)
	})
}

func TestSSOGlobalProtect(t *testing.T) {
	exe := testBrowser(t)
	noProxy(t)
	for _, variant := range []string{"headers", "callback"} {
		t.Run(variant, func(t *testing.T) {
			var srv *httptest.Server
			srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/global-protect/prelogin.esp":
					req := base64.StdEncoding.EncodeToString([]byte(srv.URL + "/idp/sso?SAMLRequest=abc"))
					fmt.Fprintf(w, `<?xml version="1.0" encoding="UTF-8" ?><prelogin-response><status>Success</status><ccusername/><autosubmit>false</autosubmit><msg/><newmsg/><authentication-message>Enter login credentials</authentication-message><username-label>Username</username-label><password-label>Password</password-label><panos-version>1</panos-version><saml-default-browser>yes</saml-default-browser><region>EU</region><saml-auth-method>REDIRECT</saml-auth-method><saml-request>%s</saml-request></prelogin-response>`, req)
				case "/idp/sso":
					w.Header().Set("Content-Type", "text/html")
					fmt.Fprintf(w, autoSubmit, "/SAML20/SP/ACS")
				case "/SAML20/SP/ACS":
					w.Header().Set("Content-Type", "text/html")
					if variant == "callback" {
						data := base64.StdEncoding.EncodeToString([]byte(`<?xml version="1.0"?><html><saml-username>alice@corp</saml-username><prelogin-cookie>PRE-C00KIE</prelogin-cookie><saml-auth-status>1</saml-auth-status></html>`))
						fmt.Fprintf(w, `<html><body>Redirecting…<script>window.location.href = "globalprotectcallback:%s";</script></body></html>`, data)
						return
					}
					w.Header().Set("saml-username", "alice@corp")
					w.Header().Set("prelogin-cookie", "PRE-C00KIE")
					w.Header().Set("saml-auth-status", "1")
					fmt.Fprint(w, "<html><body>Login successful</body></html>")
				}
			}))
			defer srv.Close()
			cfg, log := ssoCfg(t, map[string]any{"protocol": "gp", "server": srv.URL, "serverCert": spkiPin(srv)})
			cfg.Browser = exe
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			res, err := ssoLogin(ctx, cfg, "gp")
			if err != nil {
				t.Fatalf("%v\n%s", err, log.Tail(20))
			}
			if res.User != "alice@corp" || res.Cookie != "PRE-C00KIE" || res.UserGroup != "portal:prelogin-cookie" {
				t.Fatalf("result: %+v", res)
			}
		})
	}
}

func TestSSOFortinet(t *testing.T) {
	noProxy(t)
	for _, variant := range []string{"redirect-window", "redirect-system", "cookie-window"} {
		t.Run(variant, func(t *testing.T) {
			srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/remote/saml/start":
					http.Redirect(w, r, "/idp/sso", http.StatusFound)
				case "/idp/sso":
					w.Header().Set("Content-Type", "text/html")
					fmt.Fprintf(w, autoSubmit, "/remote/saml/login")
				case "/remote/saml/login":
					if variant == "cookie-window" { // older FortiOS: cookie set in the browser
						http.SetCookie(w, &http.Cookie{Name: "SVPNCOOKIE", Value: "F0RTI-BROWSER", Path: "/"})
						w.Header().Set("Content-Type", "text/html")
						fmt.Fprint(w, "<html>ok</html>")
						return
					}
					http.Redirect(w, r, "http://127.0.0.1:8020/?id=AUTH1D", http.StatusFound)
				case "/remote/saml/auth_id":
					if r.URL.Query().Get("id") != "AUTH1D" {
						http.Error(w, "bad id", 403)
						return
					}
					http.SetCookie(w, &http.Cookie{Name: "SVPNCOOKIE", Value: "F0RTI-SESSION", Path: "/"})
					fmt.Fprint(w, "ok")
				}
			}))
			defer srv.Close()
			mode := "window"
			if variant == "redirect-system" {
				mode = "system"
			}
			cfg, log := ssoCfg(t, map[string]any{"protocol": "fortinet", "server": srv.URL, "serverCert": spkiPin(srv), "ssoBrowser": mode})
			if mode == "window" {
				cfg.Browser = testBrowser(t)
			} else {
				cfg.Auth = func(p AuthPrompt) {
					if p.URL == "" {
						return
					}
					go func() {
						c := srv.Client()
						c.Jar = newJar()
						c.CheckRedirect = func(req *http.Request, via []*http.Request) error {
							if req.URL.Host == "127.0.0.1:8020" { // leave the TLS client for the callback
								res, err := http.Get(req.URL.String())
								if err == nil {
									res.Body.Close()
								}
								return http.ErrUseLastResponse
							}
							return nil
						}
						if res, err := c.Get(p.URL); err == nil {
							res.Body.Close()
						}
						if res, err := c.PostForm(srv.URL+"/remote/saml/login", url.Values{"SAMLResponse": {"ok"}}); err == nil {
							res.Body.Close()
						}
					}()
				}
			}
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			res, err := ssoLogin(ctx, cfg, "fortinet")
			if err != nil {
				t.Fatalf("%v\n%s", err, log.Tail(20))
			}
			want := "SVPNCOOKIE=F0RTI-SESSION"
			if variant == "cookie-window" {
				want = "SVPNCOOKIE=F0RTI-BROWSER"
			}
			if res.Cookie != want {
				t.Fatalf("cookie %q, want %q", res.Cookie, want)
			}
		})
	}
}

// TestSSODriver runs the whole OpenConnect driver with SSO: login through
// the login window, then openconnect presents the session cookie (and the
// STRAP key) on its tunnel request.
func TestSSODriver(t *testing.T) {
	for _, bin := range []string{"openconnect", "ocproxy"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skip(bin + " not installed")
		}
	}
	exe := testBrowser(t)
	noProxy(t)
	asa := newFakeASA(t, true)
	cfg, log := ssoCfg(t, map[string]any{"protocol": "anyconnect", "server": asa.URL, "serverCert": spkiPin(asa.Server), "sso": true, "mode": "userspace"})
	cfg.Browser, cfg.Mode = exe, ModeUserspace
	var prompts []AuthPrompt
	var pmu sync.Mutex
	cfg.Auth = func(p AuthPrompt) { pmu.Lock(); prompts = append(prompts, p); pmu.Unlock() }
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	d, _ := Get("OpenConnect")
	tun, err := d.Start(ctx, cfg)
	if err == nil {
		tun.Close()
		t.Fatal("the test gateway has no tunnel; connect should fail")
	}
	asa.mu.Lock()
	got := asa.tunnelAuth
	asa.mu.Unlock()
	if !strings.Contains(got, "webvpn=SESSION-0K-123") {
		t.Fatalf("tunnel request cookie %q\n%s", got, log.Tail(30))
	}
	pmu.Lock()
	defer pmu.Unlock()
	if len(prompts) < 2 || !prompts[0].Window || prompts[len(prompts)-1].Window {
		t.Fatalf("prompts: %+v (want login window, then cleared)", prompts)
	}
}
