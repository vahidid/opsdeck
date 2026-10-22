package vpn

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Browser is a Chrome/Edge/Chromium window opsdeck drives through the
// DevTools protocol for single sign-on: it shows the identity provider's
// login page to the user and reads the resulting session cookie (or header)
// when the login completes. Each VPN profile has its own browser profile, so
// "remember me" at the identity provider makes later logins automatic.
type Browser struct {
	cmd  *exec.Cmd
	t    cdpTransport
	sess string // page session id
	next atomic.Int64

	mu       sync.Mutex
	pending  map[int64]chan cdpMsg
	url      string // current main-frame URL
	handlers []func(method string, params json.RawMessage)
	done     chan struct{}
	closeErr error
}

type cdpMsg struct {
	ID        int64           `json:"id,omitempty"`
	Method    string          `json:"method,omitempty"`
	Params    json.RawMessage `json:"params,omitempty"`
	SessionID string          `json:"sessionId,omitempty"`
	Result    json.RawMessage `json:"result,omitempty"`
	Error     *struct {
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

// cdpTransport carries DevTools messages (a pipe on Unix, a local
// WebSocket on Windows).
type cdpTransport interface {
	send([]byte) error
	recv() ([]byte, error)
	close()
}

// browserCandidates are Chromium-family browsers, most common first.
func browserCandidates() []string {
	switch runtime.GOOS {
	case "darwin":
		return []string{
			"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
			"/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge",
			"/Applications/Chromium.app/Contents/MacOS/Chromium",
			"/Applications/Brave Browser.app/Contents/MacOS/Brave Browser",
		}
	case "windows":
		var out []string
		for _, base := range []string{os.Getenv("ProgramFiles"), os.Getenv("ProgramFiles(x86)"), os.Getenv("LocalAppData")} {
			if base == "" {
				continue
			}
			out = append(out,
				filepath.Join(base, `Google\Chrome\Application\chrome.exe`),
				filepath.Join(base, `Microsoft\Edge\Application\msedge.exe`),
				filepath.Join(base, `BraveSoftware\Brave-Browser\Application\brave.exe`),
				filepath.Join(base, `Chromium\Application\chrome.exe`))
		}
		return out
	}
	return []string{"google-chrome", "google-chrome-stable", "chromium", "chromium-browser", "microsoft-edge", "microsoft-edge-stable", "brave-browser"}
}

// FindBrowser returns the browser used for SSO: the configured path, then
// $OPSDECK_BROWSER, then the first Chromium-family browser found.
func FindBrowser(configured string) string {
	for _, p := range []string{configured, os.Getenv("OPSDECK_BROWSER")} {
		if p == "" {
			continue
		}
		if path, err := exec.LookPath(p); err == nil {
			return path
		}
	}
	for _, c := range browserCandidates() {
		if filepath.IsAbs(c) {
			if _, err := os.Stat(c); err == nil {
				return c
			}
		} else if path, err := exec.LookPath(c); err == nil {
			return path
		}
	}
	return ""
}

// LaunchBrowser opens a login window with its own profile directory.
// ignoreSPKI lists base64 SHA-256 public key pins of self-signed gateways
// the window should accept.
func LaunchBrowser(exe, profileDir string, ignoreSPKI []string) (*Browser, error) {
	if err := os.MkdirAll(profileDir, 0o700); err != nil {
		return nil, err
	}
	args := []string{
		"--user-data-dir=" + profileDir,
		"--no-first-run", "--no-default-browser-check", "--disable-default-apps",
		"--disable-features=Translate,OptimizationHints,MediaRouter",
		"--window-size=520,760",
	}
	if os.Getenv("OPSDECK_BROWSER_HEADLESS") == "1" { // tests
		args = append(args, "--headless=new")
	}
	if os.Geteuid() == 0 {
		args = append(args, "--no-sandbox") // Chrome refuses to run as root otherwise
	}
	if len(ignoreSPKI) > 0 {
		args = append(args, "--ignore-certificate-errors-spki-list="+strings.Join(ignoreSPKI, ","))
	}
	b := &Browser{pending: map[int64]chan cdpMsg{}, done: make(chan struct{})}
	var err error
	b.cmd, b.t, err = startBrowser(exe, args, profileDir)
	if err != nil {
		return nil, fmt.Errorf("start %s: %w", filepath.Base(exe), err)
	}
	go b.readLoop()
	go func() {
		err := b.cmd.Wait()
		b.mu.Lock()
		if b.closeErr == nil {
			b.closeErr = errors.New("the login window was closed")
			if err != nil {
				b.closeErr = fmt.Errorf("the login window was closed (%v)", err)
			}
		}
		b.mu.Unlock()
		b.t.close()
	}()
	if err := b.attach(); err != nil {
		b.Close()
		return nil, err
	}
	return b, nil
}

// attach finds the window's page and enables the events SSO needs.
func (b *Browser) attach() error {
	var targets struct {
		TargetInfos []struct {
			TargetID string `json:"targetId"`
			Type     string `json:"type"`
		} `json:"targetInfos"`
	}
	var targetID string
	for i := 0; i < 50 && targetID == ""; i++ {
		if err := b.call("", "Target.getTargets", nil, &targets); err != nil {
			return err
		}
		for _, t := range targets.TargetInfos {
			if t.Type == "page" {
				targetID = t.TargetID
				break
			}
		}
		if targetID == "" {
			var created struct {
				TargetID string `json:"targetId"`
			}
			if err := b.call("", "Target.createTarget", map[string]any{"url": "about:blank"}, &created); err == nil {
				targetID = created.TargetID
			}
			time.Sleep(100 * time.Millisecond)
		}
	}
	if targetID == "" {
		return errors.New("the browser opened no page")
	}
	var att struct {
		SessionID string `json:"sessionId"`
	}
	if err := b.call("", "Target.attachToTarget", map[string]any{"targetId": targetID, "flatten": true}, &att); err != nil {
		return err
	}
	b.sess = att.SessionID
	for _, m := range []string{"Page.enable", "Network.enable"} {
		if err := b.call(b.sess, m, nil, nil); err != nil {
			return err
		}
	}
	return nil
}

func (b *Browser) readLoop() {
	defer close(b.done)
	for {
		data, err := b.t.recv()
		if err != nil {
			b.mu.Lock()
			for id, ch := range b.pending {
				close(ch)
				delete(b.pending, id)
			}
			b.mu.Unlock()
			return
		}
		var m cdpMsg
		if json.Unmarshal(data, &m) != nil {
			continue
		}
		if m.ID != 0 {
			b.mu.Lock()
			ch := b.pending[m.ID]
			delete(b.pending, m.ID)
			b.mu.Unlock()
			if ch != nil {
				ch <- m
			}
			continue
		}
		if m.Method == "Page.frameNavigated" {
			var p struct {
				Frame struct {
					ParentID string `json:"parentId"`
					URL      string `json:"url"`
				} `json:"frame"`
			}
			if json.Unmarshal(m.Params, &p) == nil && p.Frame.ParentID == "" {
				b.mu.Lock()
				b.url = p.Frame.URL
				b.mu.Unlock()
			}
		}
		b.mu.Lock()
		hs := append([]func(string, json.RawMessage){}, b.handlers...)
		b.mu.Unlock()
		for _, h := range hs {
			h(m.Method, m.Params)
		}
	}
}

func (b *Browser) call(session, method string, params any, out any) error {
	id := b.next.Add(1)
	ch := make(chan cdpMsg, 1)
	b.mu.Lock()
	b.pending[id] = ch
	b.mu.Unlock()
	msg := map[string]any{"id": id, "method": method}
	if params != nil {
		msg["params"] = params
	}
	if session != "" {
		msg["sessionId"] = session
	}
	data, _ := json.Marshal(msg)
	if err := b.t.send(data); err != nil {
		return b.err(err)
	}
	select {
	case m, ok := <-ch:
		if !ok {
			return b.err(errors.New("browser connection closed"))
		}
		if m.Error != nil {
			return fmt.Errorf("%s: %s", method, m.Error.Message)
		}
		if out != nil && len(m.Result) > 0 {
			return json.Unmarshal(m.Result, out)
		}
		return nil
	case <-time.After(30 * time.Second):
		return fmt.Errorf("%s: browser did not answer", method)
	}
}

func (b *Browser) err(e error) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closeErr != nil {
		return b.closeErr
	}
	return e
}

// On registers an event handler (method, params).
func (b *Browser) On(h func(method string, params json.RawMessage)) {
	b.mu.Lock()
	b.handlers = append(b.handlers, h)
	b.mu.Unlock()
}

// Navigate loads a URL in the login window.
func (b *Browser) Navigate(url string) error {
	return b.call(b.sess, "Page.navigate", map[string]any{"url": url}, nil)
}

// SetContent shows an HTML document (e.g. a self-submitting SAML POST form).
func (b *Browser) SetContent(html string) error {
	var tree struct {
		FrameTree struct {
			Frame struct {
				ID string `json:"id"`
			} `json:"frame"`
		} `json:"frameTree"`
	}
	if err := b.call(b.sess, "Page.getFrameTree", nil, &tree); err != nil {
		return err
	}
	return b.call(b.sess, "Page.setDocumentContent", map[string]any{"frameId": tree.FrameTree.Frame.ID, "html": html}, nil)
}

// URL is the current main-frame URL.
func (b *Browser) URL() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.url
}

// Cookie returns the value of a cookie the browser would send to url.
func (b *Browser) Cookie(url, name string) string {
	var res struct {
		Cookies []struct {
			Name  string `json:"name"`
			Value string `json:"value"`
		} `json:"cookies"`
	}
	if b.call(b.sess, "Network.getCookies", map[string]any{"urls": []string{url}}, &res) != nil {
		return ""
	}
	for _, c := range res.Cookies {
		if c.Name == name {
			return c.Value
		}
	}
	return ""
}

// HTML returns the current document's markup (including comments).
func (b *Browser) HTML() string {
	var res struct {
		Result struct {
			Value string `json:"value"`
		} `json:"result"`
	}
	b.call(b.sess, "Runtime.evaluate", map[string]any{"expression": "document.documentElement ? document.documentElement.outerHTML : ''", "returnByValue": true}, &res)
	return res.Result.Value
}

// Done is closed when the browser exits.
func (b *Browser) Done() <-chan struct{} { return b.done }

// Close closes the login window.
func (b *Browser) Close() {
	b.mu.Lock()
	if b.closeErr == nil {
		b.closeErr = errors.New("closed")
	}
	b.mu.Unlock()
	done := make(chan struct{})
	go func() {
		data, _ := json.Marshal(map[string]any{"id": b.next.Add(1), "method": "Browser.close"})
		b.t.send(data)
		close(done)
	}()
	select {
	case <-b.done:
	case <-time.After(3 * time.Second):
	}
	<-done
	if b.cmd.Process != nil {
		b.cmd.Process.Kill()
	}
	b.t.close()
}

// ---------------------------------------------------------------------------

// pipeTransport talks to Chrome over --remote-debugging-pipe (fds 3 and 4):
// no port another local process could connect to.
type pipeTransport struct {
	w  io.WriteCloser
	r  *bufio.Reader
	rc io.Closer
	mu sync.Mutex
}

func (p *pipeTransport) send(b []byte) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	_, err := p.w.Write(append(b, 0))
	return err
}

func (p *pipeTransport) recv() ([]byte, error) {
	b, err := p.r.ReadBytes(0)
	if err != nil {
		return nil, err
	}
	return b[:len(b)-1], nil
}

func (p *pipeTransport) close() {
	p.w.Close()
	p.rc.Close()
}

// openURL opens a URL in the user's default browser (best effort).
func openURL(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		if os.Getenv("DISPLAY") == "" && os.Getenv("WAYLAND_DISPLAY") == "" {
			return errors.New("no desktop session")
		}
		cmd = exec.Command("xdg-open", url)
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	go cmd.Wait()
	return nil
}
