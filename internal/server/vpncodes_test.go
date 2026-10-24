package server

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"opsdeck/internal/paths"
)

// smsGateway is a FortiGate that sends a one-time code by SMS for every
// login, and records the session cookie a client presents afterwards.
type smsGateway struct {
	*httptest.Server
	mu        sync.Mutex
	code      string
	logins    int
	presented []string
}

func newSMSGateway(t *testing.T) *smsGateway {
	g := &smsGateway{code: "482913"}
	g.Server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		g.mu.Lock()
		defer g.mu.Unlock()
		if r.URL.Path != "/remote/logincheck" {
			g.presented = append(g.presented, r.Header.Get("Cookie"))
			http.Error(w, "this test gateway has no tunnel", http.StatusForbidden)
			return
		}
		r.ParseForm()
		if r.Form.Has("code") {
			if r.Form.Get("code") == g.code && r.Form.Get("reqid") == "77" {
				http.SetCookie(w, &http.Cookie{Name: "SVPNCOOKIE", Value: fmt.Sprintf("session-%d", g.logins), Path: "/", Secure: true})
				fmt.Fprint(w, "ret=1,redir=/remote/fortisslvpn_xml")
				return
			}
			fmt.Fprint(w, "ret=2,reqid=77,polid=1-1-1,grp=staff,tokeninfo=,chal_msg=Wrong code. Enter the code sent to +98912***4567")
			return
		}
		if r.Form.Get("username") != "alice" || r.Form.Get("credential") != "s3cret" {
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprint(w, "ret=0,redir=/remote/login?&err=sslvpn_login_permission_denied&lang=en")
			return
		}
		g.logins++
		fmt.Fprint(w, "ret=2,reqid=77,polid=1-1-1,grp=staff,tokeninfo=,chal_msg=Please enter the code sent to +98912***4567")
	}))
	t.Cleanup(g.Close)
	return g
}

func (g *smsGateway) sawSession(n int) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, c := range g.presented {
		if strings.Contains(c, fmt.Sprintf("SVPNCOOKIE=session-%d", n)) {
			return true
		}
	}
	return false
}

func eventually(t *testing.T, what string, timeout time.Duration, ok func() bool) {
	t.Helper()
	for end := time.Now().Add(timeout); time.Now().Before(end); time.Sleep(200 * time.Millisecond) {
		if ok() {
			return
		}
	}
	t.Fatalf("timed out waiting for %s", what)
}

// TestFortiGateSMS: connecting a FortiGate profile (OpenConnect) whose
// gateway sends a code by SMS stops for the code; the prompt the app shows
// on every page lists it with the gateway's message. The code typed with a
// Persian keyboard logs in and openconnect presents the session. With an
// ntfy topic in the settings, the SMS forwarded from the phone fills the code
// in by itself.
func TestFortiGateSMS(t *testing.T) {
	for _, bin := range []string{"openconnect", "ocproxy"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skip(bin + " not installed")
		}
	}
	for _, k := range []string{"https_proxy", "HTTPS_PROXY", "http_proxy", "HTTP_PROXY", "all_proxy", "ALL_PROXY"} {
		t.Setenv(k, "")
		os.Unsetenv(k)
	}
	t.Setenv("HOME", t.TempDir())
	t.Setenv("OPSDECK_HOME", t.TempDir())
	srv, err := New(paths.Default(), Options{Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	jar, _ := cookiejar.New(nil)
	hc := &http.Client{Jar: jar, Timeout: 60 * time.Second}
	c := &client{t: t, base: ts.URL, http: hc}
	c.must("POST", "/api/auth/login", map[string]string{"token": srv.token}, nil)
	c.must("POST", "/api/vault/init", map[string]string{"password": "correct horse battery"}, nil)
	c.must("POST", "/api/workspaces", map[string]string{"name": "acme"}, nil)

	gw := newSMSGateway(t)
	h := sha256.Sum256(gw.Certificate().RawSubjectPublicKeyInfo)
	c.must("POST", "/api/vault/items", map[string]any{"scope": "acme", "type": "login", "name": "forti",
		"fields": []map[string]any{{"name": "username", "value": "alice"}, {"name": "password", "value": "s3cret", "secret": true}}}, nil)
	c.must("POST", "/api/workspaces/acme/resources", map[string]any{"kind": "OpenConnect", "name": "office",
		"spec": map[string]any{"protocol": "fortinet", "server": gw.URL, "auth": "vault://acme/forti", "mode": "userspace",
			"serverCert": "pin-sha256:" + base64.StdEncoding.EncodeToString(h[:])}}, nil)

	connect := func() {
		go func() { // waits up to 20 s for the tunnel; the code comes meanwhile
			if res, err := hc.Post(ts.URL+"/api/workspaces/acme/networks/office/up", "application/json", strings.NewReader("{}")); err == nil {
				res.Body.Close()
			}
		}()
	}
	pending := func() []map[string]any {
		var l []map[string]any
		c.must("GET", "/api/vpn/codes", nil, &l)
		return l
	}
	status := func() map[string]any {
		var nets []map[string]any
		c.must("GET", "/api/workspaces/acme/networks", nil, &nets)
		return nets[0]
	}

	connect()
	eventually(t, "the code prompt", 20*time.Second, func() bool { return len(pending()) == 1 })
	p := pending()[0]
	if p["workspace"] != "acme" || p["name"] != "office" || p["message"] != "Please enter the code sent to +98912***4567" || p["sources"] != nil {
		t.Fatalf("prompt %v", p)
	}
	if st := status(); st["status"] != "auth" || st["code"] == nil {
		t.Fatalf("status %v", st)
	}
	if code := c.do("POST", "/api/workspaces/acme/networks/office/code", map[string]string{"code": " "}, nil); code != 400 {
		t.Fatalf("an empty code: %d", code)
	}
	// a wrong code is asked for again, with the gateway's new message
	c.must("POST", "/api/workspaces/acme/networks/office/code", map[string]string{"code": "111111"}, nil)
	eventually(t, "the second prompt", 10*time.Second, func() bool {
		l := pending()
		return len(l) == 1 && l[0]["retry"] == true && strings.HasPrefix(fmt.Sprint(l[0]["message"]), "Wrong code")
	})
	c.must("POST", "/api/workspaces/acme/networks/office/code", map[string]string{"code": "۴۸۲۹۱۳"}, nil)
	eventually(t, "openconnect presenting the session", 20*time.Second, func() bool { return gw.sawSession(1) })
	if len(pending()) != 0 {
		t.Fatalf("still prompting: %v", pending())
	}
	// the test gateway has no tunnel
	eventually(t, "the connection to fail", 30*time.Second, func() bool { return status()["status"] == "error" })
	var log []map[string]any
	c.must("GET", "/api/workspaces/acme/networks/office/log", nil, &log)
	b, _ := json.Marshal(log)
	if strings.Contains(string(b), "482913") || strings.Contains(string(b), "s3cret") || strings.Contains(string(b), "session-1") {
		t.Fatalf("the log shows a secret: %s", b)
	}
	if code := c.do("POST", "/api/workspaces/acme/networks/office/code", map[string]string{"code": "1234"}, nil); code != 400 {
		t.Fatalf("a code nobody waits for: %d", code)
	}

	// the phone forwards the SMS to an ntfy topic
	sms := make(chan string, 1)
	ntfy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/opsdeck-k3y/json" {
			http.NotFound(w, r)
			return
		}
		enc := json.NewEncoder(w)
		enc.Encode(map[string]any{"event": "open", "time": time.Now().Unix()})
		w.(http.Flusher).Flush()
		select {
		case m := <-sms:
			enc.Encode(map[string]any{"event": "message", "time": time.Now().Unix(), "message": m})
			w.(http.Flusher).Flush()
			<-r.Context().Done()
		case <-r.Context().Done():
		}
	}))
	defer ntfy.Close()
	var settings map[string]any
	c.must("GET", "/api/settings", nil, &settings)
	settings["vpn"] = map[string]any{"codes": map[string]any{"ntfyUrl": "ftp://ntfy.example.com/x"}}
	if code := c.do("PUT", "/api/settings", settings, nil); code != 400 {
		t.Fatalf("an invalid topic URL was saved: %d", code)
	}
	settings["vpn"] = map[string]any{"codes": map[string]any{"ntfyUrl": ntfy.URL + "/opsdeck-k3y/"}}
	c.must("PUT", "/api/settings", settings, &settings)
	if u := settings["vpn"].(map[string]any)["codes"].(map[string]any)["ntfyUrl"]; u != ntfy.URL+"/opsdeck-k3y" {
		t.Fatalf("saved topic %v", u)
	}
	connect()
	eventually(t, "the code prompt with the phone as a source", 20*time.Second, func() bool {
		l := pending()
		return len(l) == 1 && fmt.Sprint(l[0]["sources"]) == "[your phone (ntfy)]"
	})
	sms <- `{"from":"10004346","text":"Your FortiGate code: 482913"}`
	eventually(t, "the forwarded code to log in", 20*time.Second, func() bool { return gw.sawSession(2) })
	c.must("GET", "/api/workspaces/acme/networks/office/log", nil, &log)
	b, _ = json.Marshal(log)
	if !strings.Contains(string(b), "one-time code received from your phone (ntfy)") {
		t.Fatalf("log: %s", b)
	}
}

// oldASA is a Cisco ASA with the old login form (no XML POST): a GROUP list,
// then a one-time code challenge.
type oldASA struct {
	*httptest.Server
	mu        sync.Mutex
	presented string // Cookie of the tunnel request
}

func newOldASA(t *testing.T) *oldASA {
	g := &oldASA{}
	form := `<?xml version="1.0" encoding="UTF-8"?>
<auth id="main"><title>Login</title><message>Please enter your username and password.</message>
<form method="post" action="/+webvpn+/index.html"><input type="text" name="username" label="Username:" /><input type="password" name="password" label="Password:" />
<select name="group_list" label="GROUP:"><option value="3rd-Party">3rd-Party</option><option value="IT-Opration">IT-Opration</option><option value="Simotech">Simotech</option></select></form></auth>`
	g.Server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		g.mu.Lock()
		defer g.mu.Unlock()
		r.ParseForm()
		w.Header().Set("Content-Type", "text/xml")
		switch {
		case r.Method == "CONNECT":
			g.presented = r.Header.Get("Cookie")
			http.Error(w, "this test gateway has no tunnel", http.StatusUnauthorized)
		case r.URL.Path == "/" && r.Method == "POST":
			http.NotFound(w, r)
		case r.URL.Path == "/":
			http.Redirect(w, r, "/+webvpn+/index.html", http.StatusFound)
		case r.URL.Path == "/+webvpn+/index.html" && r.Method == "POST" && r.Form.Get("username") == "alice" && r.Form.Get("password") == "s3cret" && r.Form.Get("group_list") == "3rd-Party":
			fmt.Fprint(w, `<?xml version="1.0" encoding="UTF-8"?><auth id="challenge"><message>Enter the code sent to your phone</message>
<form method="post" action="/+webvpn+/login/challenge.html"><input type="password" name="password" label="Answer:" /></form></auth>`)
		case r.URL.Path == "/+webvpn+/login/challenge.html" && r.Form.Get("password") == "482913":
			http.SetCookie(w, &http.Cookie{Name: "webvpn", Value: "asa-session", Path: "/", Secure: true})
			fmt.Fprint(w, `<?xml version="1.0" encoding="UTF-8"?><auth id="success"><title>SSL VPN Service</title></auth>`)
		default:
			fmt.Fprint(w, form)
		}
	}))
	t.Cleanup(g.Close)
	return g
}

// TestAnyConnectGroupAndCode: an AnyConnect profile without a group stops
// for the gateway's GROUP list (a choice in the app, not filled in from the
// phone), then for the one-time code of the challenge.
func TestAnyConnectGroupAndCode(t *testing.T) {
	for _, bin := range []string{"openconnect", "ocproxy"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skip(bin + " not installed")
		}
	}
	for _, k := range []string{"https_proxy", "HTTPS_PROXY", "http_proxy", "HTTP_PROXY", "all_proxy", "ALL_PROXY"} {
		t.Setenv(k, "")
		os.Unsetenv(k)
	}
	t.Setenv("HOME", t.TempDir())
	t.Setenv("OPSDECK_HOME", t.TempDir())
	srv, err := New(paths.Default(), Options{Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	jar, _ := cookiejar.New(nil)
	hc := &http.Client{Jar: jar, Timeout: 60 * time.Second}
	c := &client{t: t, base: ts.URL, http: hc}
	c.must("POST", "/api/auth/login", map[string]string{"token": srv.token}, nil)
	c.must("POST", "/api/vault/init", map[string]string{"password": "correct horse battery"}, nil)
	c.must("POST", "/api/workspaces", map[string]string{"name": "acme"}, nil)
	// a phone forwarding codes, which never sends one here
	ntfy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"event": "open", "time": time.Now().Unix()})
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer ntfy.Close()
	var settings map[string]any
	c.must("GET", "/api/settings", nil, &settings)
	settings["vpn"] = map[string]any{"codes": map[string]any{"ntfyUrl": ntfy.URL + "/opsdeck-k3y"}}
	c.must("PUT", "/api/settings", settings, nil)

	gw := newOldASA(t)
	h := sha256.Sum256(gw.Certificate().RawSubjectPublicKeyInfo)
	c.must("POST", "/api/vault/items", map[string]any{"scope": "acme", "type": "login", "name": "asa",
		"fields": []map[string]any{{"name": "username", "value": "alice"}, {"name": "password", "value": "s3cret", "secret": true}}}, nil)
	c.must("POST", "/api/workspaces/acme/resources", map[string]any{"kind": "OpenConnect", "name": "office",
		"spec": map[string]any{"protocol": "anyconnect", "server": gw.URL, "auth": "vault://acme/asa", "mode": "userspace",
			"serverCert": "pin-sha256:" + base64.StdEncoding.EncodeToString(h[:])}}, nil)
	go func() {
		if res, err := hc.Post(ts.URL+"/api/workspaces/acme/networks/office/up", "application/json", strings.NewReader("{}")); err == nil {
			res.Body.Close()
		}
	}()
	pending := func() []map[string]any {
		var l []map[string]any
		c.must("GET", "/api/vpn/codes", nil, &l)
		return l
	}
	eventually(t, "the group prompt", 20*time.Second, func() bool { return len(pending()) == 1 })
	p := pending()[0]
	if fmt.Sprint(p["choices"]) != "[3rd-Party IT-Opration Simotech]" || p["label"] != "GROUP" || p["sources"] != nil {
		t.Fatalf("prompt %v", p)
	}
	if code := c.do("POST", "/api/workspaces/acme/networks/office/code", map[string]string{"code": "Sales"}, nil); code != 400 {
		t.Fatalf("a group the gateway does not offer: %d", code)
	}
	c.must("POST", "/api/workspaces/acme/networks/office/code", map[string]string{"code": " 3rd-party"}, nil)
	eventually(t, "the code prompt", 20*time.Second, func() bool {
		l := pending()
		return len(l) == 1 && l[0]["label"] == "Answer" && l[0]["choices"] == nil
	})
	if p := pending()[0]; p["message"] != "Enter the code sent to your phone" || fmt.Sprint(p["sources"]) != "[your phone (ntfy)]" {
		t.Fatalf("prompt %v", p)
	}
	c.must("POST", "/api/workspaces/acme/networks/office/code", map[string]string{"code": "482 913"}, nil)
	eventually(t, "openconnect presenting the session", 20*time.Second, func() bool {
		gw.mu.Lock()
		defer gw.mu.Unlock()
		return strings.Contains(gw.presented, "webvpn=asa-session")
	})
	var log []map[string]any
	c.must("GET", "/api/workspaces/acme/networks/office/log", nil, &log)
	b, _ := json.Marshal(log)
	if strings.Contains(string(b), "482913") || strings.Contains(string(b), "s3cret") || !strings.Contains(string(b), "GROUP chosen: 3rd-Party") {
		t.Fatalf("log: %s", b)
	}
}
