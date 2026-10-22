package vpn

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// testBrowser starts a headless Chromium-family browser, or skips.
func testBrowser(t *testing.T) string {
	t.Helper()
	exe := FindBrowser("")
	if exe == "" {
		t.Skip("no Chrome/Chromium (set OPSDECK_BROWSER)")
	}
	t.Setenv("OPSDECK_BROWSER_HEADLESS", "1")
	return exe
}

func TestBrowserDriver(t *testing.T) {
	exe := testBrowser(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		switch r.URL.Path {
		case "/login":
			fmt.Fprint(w, `<form action="/done" method="post"><input name="u" value="alice"></form><script>document.forms[0].submit()</script>`)
		case "/done":
			http.SetCookie(w, &http.Cookie{Name: "session", Value: "s3cr3t", Path: "/"})
			w.Header().Set("X-Login-User", "alice")
			fmt.Fprint(w, `<html><body>ok<!-- <marker>42</marker> --></body></html>`)
		}
	}))
	defer srv.Close()
	b, err := LaunchBrowser(exe, t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	var mu sync.Mutex
	headers := map[string]string{}
	b.On(func(method string, params json.RawMessage) {
		if method != "Network.responseReceived" {
			return
		}
		var p struct {
			Response struct {
				URL     string            `json:"url"`
				Headers map[string]string `json:"headers"`
			} `json:"response"`
		}
		json.Unmarshal(params, &p)
		mu.Lock()
		for k, v := range p.Response.Headers {
			headers[strings.ToLower(k)] = v
		}
		mu.Unlock()
	})
	if err := b.Navigate(srv.URL + "/login"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(15 * time.Second)
	for !strings.HasSuffix(b.URL(), "/done") && time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
	}
	if !strings.HasSuffix(b.URL(), "/done") {
		t.Fatalf("url: %q html %q", b.URL(), b.HTML())
	}
	if c := b.Cookie(srv.URL+"/", "session"); c != "s3cr3t" {
		t.Fatalf("cookie: %q", c)
	}
	mu.Lock()
	user := headers["x-login-user"]
	mu.Unlock()
	if user != "alice" {
		t.Fatalf("header: %v", headers)
	}
	if !strings.Contains(b.HTML(), "<marker>42</marker>") {
		t.Fatalf("html: %q", b.HTML())
	}
}
