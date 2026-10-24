package vpn

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"opsdeck/internal/vault"
	"opsdeck/internal/workspace"
)

// fakeFortiGate answers /remote/logincheck like a FortiGate whose users get
// a one-time code by SMS (or have no second factor), and records the
// session cookie a client presents afterwards.
type fakeFortiGate struct {
	*httptest.Server
	user, password, realm string
	sms                   bool   // ask for a code after the password
	code                  string // the code "sent by SMS"
	mu                    sync.Mutex
	logins                int      // first-stage requests
	codes                 []string // codes received
	presented             string   // Cookie of a request after the login
}

const fortiSession = "S3SS10N-c00kie"

func newFakeFortiGate(t *testing.T, sms bool) *fakeFortiGate {
	f := &fakeFortiGate{user: "alice", password: "s3cret", realm: "staff", sms: sms, code: "482913"}
	f.Server = httptest.NewTLSServer(http.HandlerFunc(f.handle))
	t.Cleanup(f.Close)
	return f
}

func (f *fakeFortiGate) handle(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.URL.Path != "/remote/logincheck" {
		f.presented = r.Header.Get("Cookie")
		http.Error(w, "this test gateway has no tunnel", http.StatusForbidden)
		return
	}
	r.ParseForm()
	if r.UserAgent() != fortiUA {
		http.Error(w, "unexpected user agent "+r.UserAgent(), http.StatusBadRequest)
		return
	}
	login := func() {
		http.SetCookie(w, &http.Cookie{Name: "SVPNCOOKIE", Value: fortiSession, Path: "/", Secure: true, HttpOnly: true})
		fmt.Fprint(w, "ret=1,redir=/remote/fortisslvpn_xml")
	}
	if code := r.Form.Get("code"); r.Form.Has("code") {
		f.codes = append(f.codes, code)
		ok := r.Form.Get("reqid") == "1234567" && r.Form.Get("magic") == "1-98765" && r.Form.Get("grp") == "vpn-users" &&
			r.Form.Get("username") == f.user && r.Form.Get("realm") == f.realm && r.Form.Has("code2")
		if ok && code == f.code {
			login()
			return
		}
		// a wrong code: the gateway asks again
		fmt.Fprint(w, "ret=2,reqid=1234567,polid=1-1-1,grp=vpn-users,portal=full,magic=1-98765,tokeninfo=,chal_msg=Wrong code, enter the code sent to +98912***4567")
		return
	}
	f.logins++
	if r.Form.Get("username") != f.user || r.Form.Get("credential") != f.password || r.Form.Get("realm") != f.realm || r.Form.Get("ajax") != "1" {
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, "ret=0,redir=/remote/login?&err=sslvpn_login_permission_denied&lang=en")
		return
	}
	if !f.sms {
		login()
		return
	}
	fmt.Fprint(w, "ret=2,reqid=1234567,polid=1-1-1,grp=vpn-users,portal=full,magic=1-98765,tokeninfo=,chal_msg=Please enter the token code sent to +98912***4567")
}

func fortiCfg(t *testing.T, f *fakeFortiGate, kind string, login map[string]string) (*Config, *Log) {
	log := NewLog()
	dir := t.TempDir()
	os.MkdirAll(dir+"/run", 0o700)
	spec := map[string]any{"server": f.URL, "authGroup": f.realm, "serverCert": spkiPin(f.Server), "auth": "vault://acme/forti"}
	if kind == "OpenConnect" {
		spec["protocol"] = "fortinet"
	}
	it := vault.Item{Type: "login"}
	for _, k := range []string{"username", "password", "totp"} {
		if v, ok := login[k]; ok {
			it.Fields = append(it.Fields, vault.Field{Name: k, Value: v})
		}
	}
	return &Config{Res: &workspace.Resource{Kind: kind, Name: "office", Spec: spec}, Items: map[string]vault.Item{"auth": it},
		StateDir: dir, RunDir: dir + "/run", Log: log}, log
}

func TestFortiLoginPassword(t *testing.T) {
	f := newFakeFortiGate(t, false)
	cfg, log := fortiCfg(t, f, "OpenConnect", map[string]string{"username": "alice", "password": "s3cret"})
	cfg.Code = func(context.Context, CodePrompt) (string, error) { t.Fatal("no code was asked for"); return "", nil }
	cookie, err := fortiLogin(context.Background(), cfg)
	if err != nil || cookie != "SVPNCOOKIE="+fortiSession {
		t.Fatalf("cookie %q, %v\n%s", cookie, err, log.Tail(20))
	}
	if !MayAskForCode(cfg.Res) {
		t.Fatal("a FortiGate password login may ask for a code")
	}
}

func TestFortiLoginWrongPassword(t *testing.T) {
	f := newFakeFortiGate(t, true)
	cfg, _ := fortiCfg(t, f, "OpenFortiVPN", map[string]string{"username": "alice", "password": "wrong"})
	_, err := fortiLogin(context.Background(), cfg)
	if err == nil || !strings.Contains(err.Error(), "login failed (sslvpn_login_permission_denied)") {
		t.Fatalf("err = %v", err)
	}
}

// TestFortiLoginSMS: the gateway sends a code by SMS; it is asked for with
// the gateway's message, a wrong one is asked for again, the right one logs in.
func TestFortiLoginSMS(t *testing.T) {
	f := newFakeFortiGate(t, true)
	cfg, log := fortiCfg(t, f, "OpenConnect", map[string]string{"username": "alice", "password": "s3cret"})
	var prompts []CodePrompt
	start := time.Now()
	cfg.Code = func(_ context.Context, p CodePrompt) (string, error) {
		prompts = append(prompts, p)
		if len(prompts) == 1 {
			return "111111", nil
		}
		return " ۴۸۲ ۹۱۳ ", nil // typed with a Persian keyboard
	}
	cookie, err := fortiLogin(context.Background(), cfg)
	if err != nil || cookie != "SVPNCOOKIE="+fortiSession {
		t.Fatalf("cookie %q, %v\n%s", cookie, err, log.Tail(20))
	}
	if len(prompts) != 2 || prompts[0].Message != "Please enter the token code sent to +98912***4567" || prompts[0].Retry || !prompts[1].Retry {
		t.Fatalf("prompts %+v", prompts)
	}
	if prompts[1].Message != "Wrong code, enter the code sent to +98912***4567" || prompts[0].Network != "office" {
		t.Fatalf("prompts %+v", prompts)
	}
	if prompts[0].Since.Before(start) || !prompts[1].Since.After(prompts[0].Since) {
		t.Fatalf("since: %v then %v", prompts[0].Since, prompts[1].Since)
	}
	if strings.Join(f.codes, ",") != "111111,482913" {
		t.Fatalf("codes sent: %v", f.codes)
	}
	if strings.Contains(log.Tail(50), "482913") || strings.Contains(log.Tail(50), "s3cret") {
		t.Fatalf("the log shows a secret:\n%s", log.Tail(50))
	}
}

// TestFortiLoginTOTP: a TOTP secret on the login answers the code request.
func TestFortiLoginTOTP(t *testing.T) {
	f := newFakeFortiGate(t, true)
	const secret = "JBSWY3DPEHPK3PXP"
	f.code, _, _, _ = vault.TOTP(secret, time.Now())
	if _, _, rem, _ := vault.TOTP(secret, time.Now()); rem < 3 {
		time.Sleep(time.Duration(rem+1) * time.Second)
		f.code, _, _, _ = vault.TOTP(secret, time.Now())
	}
	cfg, log := fortiCfg(t, f, "OpenFortiVPN", map[string]string{"username": "alice", "password": "s3cret", "totp": secret})
	cookie, err := fortiLogin(context.Background(), cfg)
	if err != nil || cookie != "SVPNCOOKIE="+fortiSession {
		t.Fatalf("cookie %q, %v\n%s", cookie, err, log.Tail(20))
	}
}

func TestFortiLoginNoWayToAsk(t *testing.T) {
	f := newFakeFortiGate(t, true)
	cfg, _ := fortiCfg(t, f, "OpenConnect", map[string]string{"username": "alice", "password": "s3cret"})
	_, err := fortiLogin(context.Background(), cfg)
	if err == nil || !strings.Contains(err.Error(), "+98912***4567") {
		t.Fatalf("err = %v", err)
	}
}

// TestFortiLoginUntrusted: a self-signed gateway without a pin fails with
// the pin to set, in the format of the profile's client.
func TestFortiLoginUntrusted(t *testing.T) {
	f := newFakeFortiGate(t, false)
	for kind, want := range map[string]string{"OpenConnect": spkiPin(f.Server), "OpenFortiVPN": fmt.Sprintf("%x", sha256Of(f.Certificate().Raw))} {
		cfg, _ := fortiCfg(t, f, kind, map[string]string{"username": "alice", "password": "s3cret"})
		delete(cfg.Res.Spec, "serverCert")
		_, err := fortiLogin(context.Background(), cfg)
		if err == nil || !strings.HasSuffix(err.Error(), "server certificate to "+want) {
			t.Fatalf("%s: err = %v, want the pin %s", kind, err, want)
		}
	}
}

// TestFortiSession: the session of the last connection is used first; when
// the gateway has ended it, opsdeck logs in again (and asks for a code).
func TestFortiSession(t *testing.T) {
	f := newFakeFortiGate(t, true)
	cfg, log := fortiCfg(t, f, "OpenConnect", map[string]string{"username": "alice", "password": "s3cret"})
	cfg.Session = &Session{}
	asked := 0
	cfg.Code = func(context.Context, CodePrompt) (string, error) { asked++; return f.code, nil }
	var started []string
	valid := map[string]bool{"SVPNCOOKIE=" + fortiSession: true}
	start := func(cookie string) (Tunnel, error) {
		started = append(started, cookie)
		if !valid[cookie] {
			return nil, errors.New("Cookie was rejected by server")
		}
		return nil, nil
	}
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		if _, err := withFortiSession(ctx, cfg, start); err != nil {
			t.Fatal(err)
		}
	}
	if asked != 1 || f.logins != 1 || len(started) != 2 {
		t.Fatalf("asked %d times, %d logins, started %v (want one login, then the same session)", asked, f.logins, started)
	}
	// the gateway ended the session
	valid = map[string]bool{}
	_, err := withFortiSession(ctx, cfg, start)
	if err == nil || asked != 2 || f.logins != 2 || cfg.Session.get(fortiSessionKey(cfg)) != "" {
		t.Fatalf("err %v, asked %d, logins %d, session %q\n%s", err, asked, f.logins, cfg.Session.get(fortiSessionKey(cfg)), log.Tail(20))
	}
	// another profile (user) does not use it
	valid["SVPNCOOKIE="+fortiSession] = true
	if _, err := withFortiSession(ctx, cfg, start); err != nil {
		t.Fatal(err)
	}
	cfg.Items["auth"].Fields[0].Value = "bob"
	if cfg.Session.get(fortiSessionKey(cfg)) != "" {
		t.Fatal("the session of alice is offered for bob")
	}
}

func sha256Of(b []byte) [32]byte { return sha256.Sum256(b) }

func TestParseFortiRet(t *testing.T) {
	kv := parseFortiRet("ret=2,reqid=99,tokeninfo=,chal_msg=Hello, enter the code sent to you,magic=1-2")
	if kv["chal_msg"] != "Hello, enter the code sent to you" || kv["reqid"] != "99" || kv["magic"] != "1-2" {
		t.Fatalf("%q", kv)
	}
	if _, ok := kv["tokeninfo"]; !ok {
		t.Fatal("tokeninfo missing")
	}
	if len(parseFortiRet("<html>")) != 0 {
		t.Fatal("not an answer")
	}
}
