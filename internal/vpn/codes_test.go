package vpn

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestFindCode(t *testing.T) {
	for msg, want := range map[string]string{
		"Your FortiToken code is: 482913":                                      "482913",
		"From: 10004346\nYour code is 482913":                                  "482913",
		`{"from":"10004346","text":"Token: 482913","sentStamp":1729760000000}`: "482913",
		"کد ورود شما: ۴۸۲۹۱۳ ‏":                                                "482913",
		"رمز یکبار مصرف ٤٨٢٩١٣":                                                "482913",
		"482913 is your verification code, valid for 300 seconds":              "482913",
		"Call +98912345678 if this was not you":                                "",
		"no digits here":                                                       "",
		"12345678901234":                                                       "",
	} {
		if got := FindCode(msg, nil); got != want {
			t.Errorf("FindCode(%q) = %q, want %q", msg, got, want)
		}
	}
	re := regexp.MustCompile(`token ([A-Z0-9]{6})`)
	if got := FindCode("your token AB12CD, not 123456", re); got != "AB12CD" {
		t.Errorf("custom pattern: %q", got)
	}
	if got := NormalizeCode(" ۱۲۳-۴۵۶‎"); got != "123456" {
		t.Errorf("NormalizeCode = %q", got)
	}
	if _, err := CompileCodePattern("("); err == nil {
		t.Error("a broken pattern compiles")
	}
}

// fakeNtfy streams a topic like ntfy: an open event with the server's time,
// then the messages since the requested duration, then new ones.
type fakeNtfy struct {
	*httptest.Server
	skew  time.Duration // the server's clock is this far ahead
	token string
	msgs  chan string // published while subscribed
	old   []ntfyEvent // cached messages
	since chan string // the since= of each subscription
}

func newFakeNtfy(t *testing.T, skew time.Duration) *fakeNtfy {
	f := &fakeNtfy{skew: skew, msgs: make(chan string, 4), since: make(chan string, 4)}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if f.token != "" && r.Header.Get("Authorization") != "Bearer "+f.token {
			http.Error(w, `{"code":40301,"error":"forbidden"}`, http.StatusForbidden)
			return
		}
		if r.URL.Path != "/opsdeck-topic/json" {
			http.NotFound(w, r)
			return
		}
		f.since <- r.URL.Query().Get("since")
		now := func() int64 { return time.Now().Add(f.skew).Unix() }
		enc := json.NewEncoder(w)
		enc.Encode(ntfyEvent{Event: "open", Time: now()})
		for _, e := range f.old {
			enc.Encode(e)
		}
		w.(http.Flusher).Flush()
		for {
			select {
			case m := <-f.msgs:
				enc.Encode(ntfyEvent{Event: "message", Time: now(), Message: m})
				w.(http.Flusher).Flush()
			case <-r.Context().Done():
				return
			}
		}
	}))
	t.Cleanup(f.Close)
	return f
}

// TestNtfySource: a message that arrived before the code was asked for is
// stale, one after it carries the code, whatever the clocks say.
func TestNtfySource(t *testing.T) {
	for _, skew := range []time.Duration{0, time.Hour, -10 * time.Minute} {
		f := newFakeNtfy(t, skew)
		f.token = "tk_secret"
		asked := time.Now().Add(-5 * time.Second)
		f.old = []ntfyEvent{
			{Event: "message", Time: asked.Add(skew - time.Minute).Unix(), Message: "Your code is 111111"},
			{Event: "keepalive", Time: asked.Add(skew).Unix()},
			{Event: "message", Time: asked.Add(skew - 2*time.Second).Unix(), Message: "no code, just a test"},
		}
		src := &NtfySource{URL: f.URL + "/opsdeck-topic", Token: f.token}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		go func() { time.Sleep(300 * time.Millisecond); f.msgs <- "Your code is ۴۸۲۹۱۳" }()
		code, err := src.Wait(ctx, CodePrompt{Since: asked})
		cancel()
		if err != nil || code != "482913" {
			t.Fatalf("skew %v: code %q, %v", skew, code, err)
		}
		if s := <-f.since; s != "35s" && s != "36s" {
			t.Fatalf("since=%s", s)
		}
		// a cached message newer than the request counts
		f.old = append(f.old, ntfyEvent{Event: "message", Time: asked.Add(skew + time.Second).Unix(), Message: "token 555666"})
		ctx, cancel = context.WithTimeout(context.Background(), 10*time.Second)
		code, err = src.Wait(ctx, CodePrompt{Since: asked})
		cancel()
		if err != nil || code != "555666" {
			t.Fatalf("skew %v: cached code %q, %v", skew, code, err)
		}
	}
}

func TestNtfySourceErrors(t *testing.T) {
	f := newFakeNtfy(t, 0)
	f.token = "tk_right"
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := (&NtfySource{URL: f.URL + "/opsdeck-topic", Token: "tk_wrong"}).Wait(ctx, CodePrompt{Since: time.Now()})
	if err == nil || !strings.Contains(err.Error(), "HTTP 403") {
		t.Fatalf("wrong token: %v", err)
	}
	_, err = (&NtfySource{URL: "http://127.0.0.1:1/topic"}).Wait(ctx, CodePrompt{Since: time.Now()})
	if err == nil || ctx.Err() != nil {
		t.Fatalf("unreachable server: %v", err)
	}
}

// TestCommandSource: the command prints nothing until the message comes,
// and gets the time the code was asked for.
func TestCommandSource(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sh")
	}
	dir := t.TempDir()
	inbox := filepath.Join(dir, "inbox")
	script := fmt.Sprintf(`echo "$OPSDECK_VPN $OPSDECK_CODE_SINCE" > %[1]s.env; [ -f %[1]s ] && cat %[1]s; true`, inbox)
	asked := time.Unix(1729760000, 0)
	go func() {
		time.Sleep(4 * time.Second)
		os.WriteFile(inbox, []byte("SMS from 10004346: code 482913\n"), 0o600)
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	code, err := (&CommandSource{Command: script}).Wait(ctx, CodePrompt{Network: "office", Since: asked})
	if err != nil || code != "482913" {
		t.Fatalf("code %q, %v", code, err)
	}
	if env, _ := os.ReadFile(inbox + ".env"); strings.TrimSpace(string(env)) != "office 1729760000" {
		t.Fatalf("environment: %q", env)
	}
	_, err = (&CommandSource{Command: "echo broken >&2; exit 3"}).Wait(ctx, CodePrompt{Since: asked})
	if err == nil || !strings.Contains(err.Error(), "broken") {
		t.Fatalf("a failing command: %v", err)
	}
}
