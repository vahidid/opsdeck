package vpn

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"opsdeck/internal/vault"
	"opsdeck/internal/workspace"
)

// OpenVPN runs the official openvpn binary and controls it through its
// management interface (a password-protected socket on 127.0.0.1): opsdeck
// answers credential prompts (username/password, private key passphrase,
// static-challenge OTP from the vault's TOTP secret) and follows the state
// and byte counters. There is no trustworthy pure-Go OpenVPN client, and
// openvpn always needs a TUN device, so only system mode exists.

func init() {
	register(&Driver{
		Kind: "OpenVPN", Label: "OpenVPN", Modes: []string{ModeSystem},
		Needs: func(*workspace.Resource, string) []string { return []string{"openvpn"} },
		Start: startOpenVPN,
	})
}

type openVPN struct {
	cfg  *Config
	p    *proc
	mgmt net.Conn
	wmu  sync.Mutex

	mu     sync.Mutex
	info   Info
	state  string
	up     chan struct{}
	upOnce sync.Once
	fail   error
}

func startOpenVPN(ctx context.Context, cfg *Config) (Tunnel, error) {
	conf := cfg.Value("config", "config")
	if conf == "" {
		conf = cfg.Value("config", "content") // a "file" item holding the .ovpn
	}
	if conf == "" {
		return nil, errors.New("no OpenVPN config: set the profile's config to a vault item holding the .ovpn (with inline certificates)")
	}
	confPath := filepath.Join(cfg.RunDir, "client.ovpn")
	if err := writePrivate(confPath, conf); err != nil {
		return nil, err
	}
	pw := randomHex(16)
	pwPath := filepath.Join(cfg.RunDir, "mgmt.pw")
	if err := writePrivate(pwPath, pw+"\n"); err != nil {
		return nil, err
	}
	port, err := freePort()
	if err != nil {
		return nil, err
	}
	scripts := "1" // no external scripts unless the profile allows them (they run as root)
	if cfg.Res.Bool("allowScripts", false) {
		scripts = "2"
	}
	argv := []string{"openvpn", "--config", confPath,
		"--management", "127.0.0.1", strconv.Itoa(port), pwPath,
		"--management-hold", "--management-query-passwords", "--auth-retry", "nointeract",
		"--auth-nocache", "--script-security", scripts, "--verb", "3", "--suppress-timestamps"}
	if runtime.GOOS == "linux" { // a recognisable interface name (macOS only allows utunN)
		argv = append(argv, "--dev", ifaceName("ovpn-", cfg.Res.Name), "--dev-type", "tun")
	}
	t := &openVPN{cfg: cfg, up: make(chan struct{})}
	t.p, err = startProc(argv, cfg.Elevate, "", cfg.Log, nil)
	if err != nil {
		return nil, err
	}
	// connect to the management interface
	deadline := time.Now().Add(15 * time.Second)
	for {
		t.mgmt, err = net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), time.Second)
		if err == nil {
			break
		}
		select {
		case <-t.p.done.Done():
			return nil, t.p.done.Err()
		case <-time.After(200 * time.Millisecond):
		}
		if time.Now().After(deadline) {
			t.p.stop(3 * time.Second)
			return nil, fmt.Errorf("openvpn management interface did not open: %v", err)
		}
	}
	go t.readMgmt(pw)
	select {
	case <-t.up:
		t.mu.Lock()
		defer t.mu.Unlock()
		if t.fail != nil {
			t.Close()
			return nil, t.fail
		}
		return t, nil
	case <-t.p.done.Done():
		return nil, t.p.done.Err()
	case <-ctx.Done():
		t.Close()
		return nil, fmt.Errorf("openvpn did not connect: %w (state %s)", ctx.Err(), t.curState())
	}
}

func (t *openVPN) curState() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.state == "" {
		return "starting"
	}
	return t.state
}

func (t *openVPN) send(cmd string) {
	t.wmu.Lock()
	defer t.wmu.Unlock()
	t.mgmt.Write([]byte(cmd + "\n"))
}

// quote escapes a value for the management protocol.
func quote(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

func (t *openVPN) finishUp(err error) {
	t.upOnce.Do(func() {
		t.mu.Lock()
		t.fail = err
		t.mu.Unlock()
		close(t.up)
	})
}

func (t *openVPN) readMgmt(pw string) {
	r := bufio.NewReader(t.mgmt)
	auth := t.cfg.Items["auth"]
	// The password prompt has no newline.
	prompt := make([]byte, len("ENTER PASSWORD:"))
	t.mgmt.SetReadDeadline(time.Now().Add(10 * time.Second))
	if _, err := io.ReadFull(r, prompt); err != nil || string(prompt) != "ENTER PASSWORD:" {
		t.finishUp(fmt.Errorf("unexpected openvpn management greeting %q: %v", prompt, err))
		return
	}
	t.mgmt.SetReadDeadline(time.Time{})
	t.send(pw)
	t.send("state on")
	t.send("bytecount 5")
	t.send("hold release")
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		switch {
		case strings.HasPrefix(line, ">HOLD:"):
			t.send("hold release")
		case strings.HasPrefix(line, ">PASSWORD:Verification Failed"):
			t.cfg.Log.Printf("authentication failed")
			t.finishUp(errors.New("OpenVPN authentication failed: check the username/password (and OTP) in the profile's login"))
		case strings.HasPrefix(line, ">PASSWORD:Need 'Auth'"):
			user, pass := auth.Get("username"), auth.Get("password")
			if user == "" && pass == "" {
				t.finishUp(errors.New("the server asks for a username and password: set the profile's login (a vault login item)"))
				continue
			}
			if i := strings.Index(line, "SC:"); i >= 0 { // static challenge, usually an OTP
				code, err := otp(auth)
				if err != nil {
					t.finishUp(fmt.Errorf("the server asks for a one-time code: %v", err))
					continue
				}
				pass = "SCRV1:" + base64.StdEncoding.EncodeToString([]byte(pass)) + ":" + base64.StdEncoding.EncodeToString([]byte(code))
			}
			t.send("username \"Auth\" " + quote(user))
			t.send("password \"Auth\" " + quote(pass))
		case strings.HasPrefix(line, ">PASSWORD:Need 'Private Key'"):
			pass := auth.Get("passphrase")
			if pass == "" {
				pass = auth.Get("password")
			}
			if pass == "" {
				t.finishUp(errors.New("the private key in the config is encrypted: put its passphrase in the profile's login (password field)"))
				continue
			}
			t.send("password \"Private Key\" " + quote(pass))
		case strings.HasPrefix(line, ">BYTECOUNT:"):
			f := strings.Split(strings.TrimPrefix(line, ">BYTECOUNT:"), ",")
			if len(f) == 2 {
				rx, _ := strconv.ParseInt(f[0], 10, 64)
				tx, _ := strconv.ParseInt(f[1], 10, 64)
				t.mu.Lock()
				t.info.RxBytes, t.info.TxBytes = rx, tx
				t.mu.Unlock()
			}
		case strings.HasPrefix(line, ">STATE:"):
			// >STATE:time,STATE,desc,local_ip,remote_ip,remote_port,...
			f := strings.Split(strings.TrimPrefix(line, ">STATE:"), ",")
			if len(f) < 2 {
				continue
			}
			t.mu.Lock()
			t.state = f[1]
			t.info.Detail = strings.ToLower(f[1])
			if f[1] == "CONNECTED" && len(f) >= 5 {
				t.info.Address, t.info.Server = f[3], f[4]
				if runtime.GOOS == "linux" {
					t.info.Interface = ifaceName("ovpn-", t.cfg.Res.Name)
				}
			}
			t.mu.Unlock()
			t.cfg.Log.Printf("state: %s", strings.Join(f[1:], " "))
			if f[1] == "CONNECTED" {
				if len(f) >= 3 && f[2] != "SUCCESS" {
					t.cfg.Log.Printf("connected with warnings: %s", f[2])
				}
				t.finishUp(nil)
			}
		case strings.HasPrefix(line, ">FATAL:"):
			t.finishUp(errors.New("openvpn: " + strings.TrimPrefix(line, ">FATAL:")))
		}
	}
}

func (t *openVPN) Dial(ctx context.Context, network, addr string) (net.Conn, error) {
	return direct(ctx, network, addr)
}

func (t *openVPN) Info() Info {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.info
}

func (t *openVPN) Done() <-chan struct{} { return t.p.done.Done() }
func (t *openVPN) Err() error            { return t.p.done.Err() }

func (t *openVPN) Close() error {
	if t.mgmt != nil {
		t.send("signal SIGTERM")
	}
	select {
	case <-t.p.done.Done():
	case <-time.After(5 * time.Second):
		t.p.stop(5 * time.Second)
	}
	if t.mgmt != nil {
		t.mgmt.Close()
	}
	removeAll(t.cfg.RunDir)
	return nil
}

func randomHex(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// otp returns the current code of a login item's TOTP secret.
func otp(it vault.Item) (string, error) {
	secret := it.Get("totp")
	if secret == "" {
		return "", errors.New("add the TOTP secret to the profile's login item, or connect from the vendor client")
	}
	code, _, _, err := vault.TOTP(secret, time.Now())
	return code, err
}
