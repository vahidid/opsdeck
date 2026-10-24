package vpn

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"opsdeck/internal/vault"
)

// fakeOldASA is a Cisco ASA that only speaks the old (non-XML POST) login,
// like many in the wild: the XML POST gets 404, the login form at
// /+webvpn+/index.html has a GROUP list, and users of one group answer a
// one-time code challenge (RADIUS). A wrong code starts the login over.
type fakeOldASA struct {
	*httptest.Server
	user, password string
	groups         []string
	group          string // the group to log in to
	code           func() string
	mu             sync.Mutex
	codes          []string // codes received
	tunnelAuth     string   // Cookie of the client's CONNECT request
}

const oldASASession = "0LD-ASA-SESSION"

func newFakeOldASA(t *testing.T, code func() string) *fakeOldASA {
	f := &fakeOldASA{user: "a.torkaman", password: "s3cret", groups: []string{"3rd-Party", "CallCenter", "IT-Opration", "Simotech"}, group: "IT-Opration", code: code}
	f.Server = httptest.NewTLSServer(http.HandlerFunc(f.handle))
	t.Cleanup(f.Close)
	return f
}

func (f *fakeOldASA) loginForm(w http.ResponseWriter, errMsg string) {
	var opts strings.Builder
	for _, g := range f.groups {
		fmt.Fprintf(&opts, `<option value="%s">%s</option>`, g, g)
	}
	if errMsg != "" {
		errMsg = "<error>" + errMsg + "</error>"
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<?xml version="1.0" encoding="UTF-8"?>
<auth id="main"><title>Login</title>%s<message>Please enter your username and password.</message>
<form method="post" action="/+webvpn+/index.html">
<input type="text" name="username" label="Username:" />
<input type="password" name="password" label="Password:" />
<select name="group_list" label="GROUP:">%s</select>
<input type="submit" name="Login" value="Login" />
</form></auth>`, errMsg, opts.String())
}

func (f *fakeOldASA) handle(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Method == "CONNECT" {
		f.tunnelAuth = r.Header.Get("Cookie")
		http.Error(w, "this test gateway has no tunnel", http.StatusUnauthorized)
		return
	}
	r.ParseForm()
	switch {
	case r.URL.Path == "/" && r.Method == "POST":
		http.NotFound(w, r) // no XML POST
	case r.URL.Path == "/":
		http.Redirect(w, r, "/+webvpn+/index.html", http.StatusFound)
	case r.URL.Path == "/+webvpn+/index.html" && r.Method == "GET":
		f.loginForm(w, "")
	case r.URL.Path == "/+webvpn+/index.html":
		if r.Form.Get("username") != f.user || r.Form.Get("password") != f.password || r.Form.Get("group_list") != f.group {
			f.loginForm(w, "Login failed.")
			return
		}
		w.Header().Set("Content-Type", "text/xml")
		fmt.Fprint(w, `<?xml version="1.0" encoding="UTF-8"?>
<auth id="challenge"><title>Challenge</title><message>Enter the verification code from your authenticator app</message>
<form method="post" action="/+webvpn+/login/challenge.html">
<input type="password" name="password" label="Response:" />
<input type="hidden" name="challenge_id" value="7" />
<input type="submit" name="Continue" value="Continue" />
</form></auth>`)
	case r.URL.Path == "/+webvpn+/login/challenge.html":
		code := r.Form.Get("password")
		f.codes = append(f.codes, code)
		if code != f.code() || r.Form.Get("challenge_id") != "7" {
			f.loginForm(w, "Login failed.")
			return
		}
		http.SetCookie(w, &http.Cookie{Name: "webvpn", Value: oldASASession, Path: "/", Secure: true})
		w.Header().Set("Content-Type", "text/xml")
		fmt.Fprint(w, `<?xml version="1.0" encoding="UTF-8"?><auth id="success"><title>SSL VPN Service</title></auth>`)
	default:
		http.NotFound(w, r)
	}
}

func (f *fakeOldASA) result() (codes []string, tunnel string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string{}, f.codes...), f.tunnelAuth
}

func needOpenConnect(t *testing.T) {
	for _, bin := range []string{"openconnect", "ocproxy"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skip(bin + " not installed")
		}
	}
}

// connectOldASA runs the OpenConnect driver against the gateway; the test
// gateway has no tunnel, so it fails after the login.
func connectOldASA(t *testing.T, f *fakeOldASA, spec map[string]any, login map[string]string, code func(CodePrompt) (string, error)) (*Log, error) {
	cfg, log := ssoCfg(t, spec)
	cfg.Res.Spec["protocol"], cfg.Res.Spec["server"], cfg.Res.Spec["serverCert"] = "anyconnect", f.URL, spkiPin(f.Server)
	cfg.Mode = ModeUserspace
	it := vault.Item{Type: "login"}
	for k, v := range login {
		it.Fields = append(it.Fields, vault.Field{Name: k, Value: v, Secret: k != "username"})
	}
	cfg.Items = map[string]vault.Item{"auth": it}
	if code != nil {
		cfg.Code = func(_ context.Context, p CodePrompt) (string, error) { return code(p) }
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	d, _ := Get("OpenConnect")
	tun, err := d.Start(ctx, cfg)
	if err == nil {
		tun.Close()
		t.Fatal("the test gateway has no tunnel; connect should fail")
	}
	return log, err
}

func TestOpenConnectGroupAndTOTP(t *testing.T) {
	needOpenConnect(t)
	noProxy(t)
	const secret = "JBSWY3DPEHPK3PXP"
	totpCode := func() string { c, _, _, _ := vault.TOTP(secret, time.Now()); return c }
	f := newFakeOldASA(t, totpCode)
	var prompts []CodePrompt
	log, err := connectOldASA(t, f, map[string]any{}, map[string]string{"username": f.user, "password": f.password, "totp": secret},
		func(p CodePrompt) (string, error) {
			prompts = append(prompts, p)
			return "it-opration", nil // as typed: the case does not matter
		})
	codes, tunnel := f.result()
	if !strings.Contains(tunnel, "webvpn="+oldASASession) {
		t.Fatalf("tunnel request cookie %q (%v)\n%s", tunnel, err, log.Tail(40))
	}
	if len(codes) != 1 || codes[0] != totpCode() {
		t.Errorf("codes %v, want the TOTP code", codes)
	}
	if len(prompts) != 1 || strings.Join(prompts[0].Choices, ",") != "3rd-Party,CallCenter,IT-Opration,Simotech" || prompts[0].Label != "GROUP" ||
		prompts[0].Message != "Please enter your username and password." {
		t.Errorf("prompts %+v, want one choice of the group", prompts)
	}
	if all := log.Tail(0); strings.Contains(all, secret) || strings.Contains(all, f.password) {
		t.Errorf("the log shows a secret:\n%s", all)
	}
}

func TestOpenConnectCodeFromUser(t *testing.T) {
	needOpenConnect(t)
	noProxy(t)
	f := newFakeOldASA(t, func() string { return "482913" })
	var prompts []CodePrompt
	log, err := connectOldASA(t, f, map[string]any{"authGroup": "IT-Opration"}, map[string]string{"username": f.user, "password": f.password},
		func(p CodePrompt) (string, error) {
			prompts = append(prompts, p)
			if len(prompts) == 1 {
				return "000000", nil // wrong: the gateway starts over
			}
			return "۴۸۲ ۹۱۳", nil // Persian digits and a space
		})
	codes, tunnel := f.result()
	if !strings.Contains(tunnel, "webvpn="+oldASASession) {
		t.Fatalf("tunnel request cookie %q (%v)\n%s", tunnel, err, log.Tail(40))
	}
	if !strings.Contains(err.Error(), "Cookie was rejected") {
		t.Errorf("error %v, want the tunnel's (the login worked)", err)
	}
	if strings.Join(codes, ",") != "000000,482913" {
		t.Errorf("codes %v", codes)
	}
	if len(prompts) != 2 || prompts[0].Retry || !prompts[1].Retry || len(prompts[0].Choices) > 0 ||
		prompts[0].Label != "Response" || prompts[0].Message != "Enter the verification code from your authenticator app" {
		t.Errorf("prompts %+v, want a code, then again (retry)", prompts)
	}
}

func TestOpenConnectWrongPassword(t *testing.T) {
	needOpenConnect(t)
	noProxy(t)
	f := newFakeOldASA(t, func() string { return "482913" })
	asked := 0
	start := time.Now()
	log, err := connectOldASA(t, f, map[string]any{"authGroup": "IT-Opration"}, map[string]string{"username": f.user, "password": "wrong"},
		func(p CodePrompt) (string, error) { asked++; return "1", nil })
	if err == nil || !strings.Contains(err.Error(), "login failed") || asked != 0 || time.Since(start) > 20*time.Second {
		t.Fatalf("err %v, asked %d times, after %v\n%s", err, asked, time.Since(start), log.Tail(40))
	}
}

func TestOpenConnectNoCodePrompt(t *testing.T) {
	needOpenConnect(t)
	noProxy(t)
	f := newFakeOldASA(t, func() string { return "482913" })
	// without a way to ask (and no group set): the error names the groups
	log, err := connectOldASA(t, f, map[string]any{}, map[string]string{"username": f.user, "password": f.password}, nil)
	if err == nil || !strings.Contains(err.Error(), "IT-Opration") || !strings.Contains(err.Error(), "group") {
		t.Fatalf("err %v\n%s", err, log.Tail(40))
	}
}
