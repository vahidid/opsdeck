package server

import (
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"opsdeck/internal/paths"
)

func TestDesktopEndpoints(t *testing.T) {
	home := t.TempDir()
	t.Setenv("OPSDECK_HOME", filepath.Join(home, "opsdeck"))
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	var opened, revealed []string
	origOpen, origReveal := openInBrowser, revealFile
	defer func() { openInBrowser, revealFile = origOpen, origReveal }()
	openInBrowser = func(u string) error { opened = append(opened, u); return nil }
	revealFile = func(p string) error { revealed = append(revealed, p); return nil }

	start := func(desktop bool) *client {
		srv, err := New(paths.Default(), Options{Port: 0, Desktop: desktop})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(srv.Close)
		ts := httptest.NewServer(srv.Handler())
		t.Cleanup(ts.Close)
		jar, _ := cookiejar.New(nil)
		c := &client{t: t, base: ts.URL, http: &http.Client{Jar: jar, Timeout: 10 * time.Second}}
		c.must("POST", "/api/auth/login", map[string]string{"token": srv.token}, nil)
		return c
	}

	// not served outside the desktop app
	if code := start(false).do("POST", "/api/desktop/open", map[string]string{"url": "https://example.com"}, nil); code != 404 {
		t.Fatalf("desktop endpoint served by the plain server: %d", code)
	}

	c := start(true)
	c.must("POST", "/api/desktop/open", map[string]string{"url": "https://example.com/a?b=c"}, nil)
	for _, bad := range []string{"file:///etc/passwd", "javascript:alert(1)", "/relative", "calc.exe"} {
		if code := c.do("POST", "/api/desktop/open", map[string]string{"url": bad}, nil); code != 400 {
			t.Fatalf("opened %q: %d", bad, code)
		}
	}
	if len(opened) != 1 || opened[0] != "https://example.com/a?b=c" {
		t.Fatalf("opened = %q", opened)
	}

	save := func(name, body string) string {
		res, err := c.http.Post(c.base+"/api/desktop/save?name="+name, "application/octet-stream", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != 200 {
			t.Fatalf("save %q: %d", name, res.StatusCode)
		}
		return revealed[len(revealed)-1]
	}
	p1 := save("app.log", "one")
	p2 := save("app.log", "two")
	p3 := save("..%2F..%2Fescape.txt", "three")
	want := filepath.Join(home, "Downloads")
	for _, p := range []string{p1, p2, p3} {
		if filepath.Dir(p) != want {
			t.Fatalf("saved outside downloads: %s", p)
		}
	}
	if filepath.Base(p2) != "app (2).log" || filepath.Base(p3) != "escape.txt" {
		t.Fatalf("names: %s, %s", filepath.Base(p2), filepath.Base(p3))
	}
	if b, _ := os.ReadFile(p1); string(b) != "one" {
		t.Fatalf("content: %q", b)
	}
}

func TestSafeFileName(t *testing.T) {
	for in, want := range map[string]string{
		"":                 "download",
		"../../x.yaml":     "x.yaml",
		`C:\Users\a\b.txt`: "b.txt",
		"a:b?.log":         "a_b_.log",
		"..":               "download",
		"topology (1).md":  "topology (1).md",
		"tab\there.txt":    "tab_here.txt",
	} {
		if got := safeFileName(in); got != want {
			t.Errorf("safeFileName(%q) = %q, want %q", in, got, want)
		}
	}
}
