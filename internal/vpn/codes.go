package vpn

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// One-time codes a gateway sends for every login, by SMS or email, which no
// TOTP secret can produce. A driver asks for one through Config.Code; opsdeck
// shows a prompt in the app, and the code can also arrive by itself
// (CodeSource): forwarded from the phone through an ntfy topic, or printed by
// a command on this computer.

// CodePrompt asks for a one-time code.
type CodePrompt struct {
	Network string    // the VPN profile
	Message string    // the gateway's prompt, e.g. "Please enter the code sent to +98912***4567"
	Retry   bool      // the code entered before was not accepted
	Since   time.Time // when the code was requested: codes in earlier messages are stale
}

// CodeSource delivers one-time codes without typing them.
type CodeSource interface {
	Name() string
	// Wait returns the code of the first message that arrives after p.Since.
	Wait(ctx context.Context, p CodePrompt) (string, error)
}

// asciiDigits turns Persian, Arabic-Indic and full-width digits into 0-9.
func asciiDigits(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= '۰' && r <= '۹':
			return '0' + r - '۰'
		case r >= '٠' && r <= '٩':
			return '0' + r - '٠'
		case r >= '０' && r <= '９':
			return '0' + r - '０'
		}
		return r
	}, s)
}

// NormalizeCode turns a typed or received code into what the gateway
// expects: ASCII digits, without spaces, dashes or direction marks.
func NormalizeCode(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) || r == '-' || unicode.Is(unicode.Cf, r) {
			return -1
		}
		return r
	}, asciiDigits(s))
}

// CompileCodePattern compiles a custom pattern for codes in messages ("" =
// the default: 4 to 8 digits, preferring a number after a word like "code").
func CompileCodePattern(s string) (*regexp.Regexp, error) {
	if strings.TrimSpace(s) == "" {
		return nil, nil
	}
	re, err := regexp.Compile(s)
	if err != nil {
		return nil, fmt.Errorf("code pattern: %v", err)
	}
	return re, nil
}

// codeWordRe marks the number that follows it as the code, ahead of other
// numbers in the message (a sender like 10004346, a validity, a date).
var codeWordRe = regexp.MustCompile(`(?i)(code|token|otp|pin|pass|کد|رمز|گذرواژه|كود)[^0-9]{0,20}$`)

// FindCode returns the one-time code in a message (an SMS, perhaps wrapped
// in JSON by the app that forwarded it). With a pattern, its first group
// (or whole match) is the code; without one, a number of 4 to 8 digits, the
// first after a word like "code" if there is one.
func FindCode(msg string, pattern *regexp.Regexp) string {
	text := asciiDigits(messageText(msg))
	if pattern != nil {
		m := pattern.FindStringSubmatch(text)
		if m == nil {
			return ""
		}
		for _, g := range m[1:] {
			if g != "" {
				return NormalizeCode(g)
			}
		}
		return NormalizeCode(m[0])
	}
	first := ""
	for i := 0; i < len(text); {
		if text[i] < '0' || text[i] > '9' {
			i++
			continue
		}
		j := i
		for j < len(text) && text[j] >= '0' && text[j] <= '9' {
			j++
		}
		// a phone number is not a code
		if n := j - i; n >= 4 && n <= 8 && (i == 0 || text[i-1] != '+') {
			if codeWordRe.MatchString(text[:i]) {
				return text[i:j]
			}
			if first == "" {
				first = text[i:j]
			}
		}
		i = j
	}
	return first
}

// messageText is the text of a message that a forwarding app may have sent
// as JSON ({"from": "…", "text": "…"}).
func messageText(s string) string {
	t := strings.TrimSpace(s)
	if !strings.HasPrefix(t, "{") {
		return s
	}
	var m map[string]any
	if json.Unmarshal([]byte(t), &m) != nil {
		return s
	}
	for _, want := range []string{"text", "message", "body", "msg", "content", "sms"} {
		for k, v := range m {
			if str, ok := v.(string); ok && str != "" && strings.EqualFold(k, want) {
				return str
			}
		}
	}
	return s
}

// ---------------------------------------------------------------------------

// NtfySource receives codes forwarded from the phone through an ntfy topic
// (ntfy.sh or a self-hosted server): an SMS forwarding app on Android, or a
// Shortcuts automation on iPhone, posts the messages of the VPN's sender to
// the topic URL, and opsdeck reads the topic while it waits for a code.
type NtfySource struct {
	URL     string         // topic URL, e.g. https://ntfy.sh/opsdeck-…
	Token   string         // access token of a protected topic (optional)
	Pattern *regexp.Regexp // nil = FindCode's default
}

func (s *NtfySource) Name() string { return "your phone (ntfy)" }

type ntfyEvent struct {
	Event   string `json:"event"`
	Time    int64  `json:"time"`
	Title   string `json:"title"`
	Message string `json:"message"`
}

// Wait subscribes to the topic. A stream that breaks is opened again; one
// that cannot be opened at all is an error (wrong URL, token or network).
func (s *NtfySource) Wait(ctx context.Context, p CodePrompt) (string, error) {
	for delay, opened := time.Second, false; ; delay = min(2*delay, 30*time.Second) {
		code, answered, err := s.listen(ctx, p)
		if code != "" {
			return code, nil
		}
		opened = opened || answered
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		if !opened {
			return "", err
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(delay):
		}
	}
}

// listen reads the topic until a code arrives or the stream ends; opened
// says whether the server answered.
func (s *NtfySource) listen(ctx context.Context, p CodePrompt) (code string, opened bool, err error) {
	// "since" as a duration counts on the server's clock, so a message that
	// arrived before opsdeck subscribed is not missed whatever this
	// computer's clock says; older ones are dropped below.
	back := int(time.Since(p.Since).Seconds()) + 30
	req, err := http.NewRequestWithContext(ctx, "GET", strings.TrimRight(s.URL, "/")+"/json?since="+strconv.Itoa(back)+"s", nil)
	if err != nil {
		return "", false, err
	}
	if s.Token != "" {
		req.Header.Set("Authorization", "Bearer "+s.Token)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", false, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(res.Body, 512))
		return "", false, fmt.Errorf("%s answered HTTP %d: %s", s.URL, res.StatusCode, strings.TrimSpace(string(b)))
	}
	sc := bufio.NewScanner(res.Body)
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	var skew time.Duration // the server's clock minus this computer's
	for sc.Scan() {
		var e ntfyEvent
		if json.Unmarshal(sc.Bytes(), &e) != nil {
			continue
		}
		switch e.Event {
		case "open":
			if e.Time > 0 {
				skew = time.Until(time.Unix(e.Time, 0))
			}
		case "message":
			if e.Time > 0 && time.Unix(e.Time, 0).Add(-skew).Before(p.Since.Add(-3*time.Second)) {
				continue // an older message
			}
			if c := FindCode(e.Message+"\n"+e.Title, s.Pattern); c != "" {
				return c, true, nil
			}
		}
	}
	if err := sc.Err(); err != nil {
		return "", true, err
	}
	return "", true, errors.New("the stream ended")
}

// ---------------------------------------------------------------------------

// CommandSource runs a command on this computer that prints the code, e.g.
// one that reads the newest SMS from a USB modem (mmcli), KDE Connect or the
// Messages database of a Mac. opsdeck runs it every few seconds while it
// waits, with OPSDECK_CODE_SINCE (the Unix time the code was requested: print
// only codes of newer messages, or nothing yet), OPSDECK_CODE_MESSAGE and
// OPSDECK_VPN in its environment.
type CommandSource struct {
	Command string
	Pattern *regexp.Regexp
}

func (s *CommandSource) Name() string { return "the code command" }

// Wait runs the command until it prints a code. A command that fails the
// first time is an error (it would fail every time).
func (s *CommandSource) Wait(ctx context.Context, p CodePrompt) (string, error) {
	for first := true; ; first = false {
		code, err := s.run(ctx, p)
		if code != "" {
			return code, nil
		}
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		if err != nil && first {
			return "", err
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(3 * time.Second):
		}
	}
}

func (s *CommandSource) run(ctx context.Context, p CodePrompt) (string, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	cmd := shellCommand(ctx, s.Command)
	cmd.Env = append(os.Environ(),
		"OPSDECK_CODE_SINCE="+strconv.FormatInt(p.Since.Unix(), 10),
		"OPSDECK_CODE_MESSAGE="+p.Message,
		"OPSDECK_VPN="+p.Network)
	out, err := cmd.StdoutPipe()
	if err != nil {
		return "", err
	}
	var stderr bytes.Buffer
	cmd.Stderr = &limitWriter{w: &stderr, n: 2048}
	if err := cmd.Start(); err != nil {
		return "", err
	}
	sc := bufio.NewScanner(out)
	for sc.Scan() {
		if code := FindCode(sc.Text(), s.Pattern); code != "" {
			cancel()
			cmd.Wait()
			return code, nil
		}
	}
	if err := cmd.Wait(); err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return "", fmt.Errorf("%v: %s", err, msg)
		}
		return "", err
	}
	return "", nil
}

// limitWriter keeps the first n bytes written to it.
type limitWriter struct {
	w io.Writer
	n int
}

func (l *limitWriter) Write(p []byte) (int, error) {
	if l.n > 0 {
		k := min(len(p), l.n)
		l.w.Write(p[:k])
		l.n -= k
	}
	return len(p), nil
}
