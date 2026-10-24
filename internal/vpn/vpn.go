// Package vpn connects opsdeck to private networks. Each VPN type is a driver
// that starts a tunnel in one of two modes:
//
//   - userspace: the TCP/IP stack runs inside opsdeck (gVisor netstack) or in
//     a helper that exposes a local SOCKS proxy. No root, no change to the
//     rest of the system, and many tunnels can be up side by side. opsdeck
//     dials through the tunnel; other apps can use it through an optional
//     local SOCKS5 proxy.
//   - system: a TUN interface with system routes and DNS, like the vendor
//     client. Needs root or CAP_NET_ADMIN (subprocesses can be run through
//     sudo/pkexec); every app on the machine uses the VPN, and several
//     system VPNs may conflict on routes and DNS.
//
// Native drivers run in-process (WireGuard, Tailscale tsnet, NetBird embed);
// the others drive the official binaries (openvpn with its management
// interface, openconnect for AnyConnect/ocserv/Fortinet/GlobalProtect/Pulse/
// F5/Array, openfortivpn).
package vpn

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"opsdeck/internal/vault"
	"opsdeck/internal/workspace"
)

// Modes.
const (
	ModeUserspace = "userspace"
	ModeSystem    = "system"
)

// DialFunc dials an address through a tunnel.
type DialFunc func(ctx context.Context, network, addr string) (net.Conn, error)

// Config is everything a driver needs to start one profile.
type Config struct {
	Res      *workspace.Resource
	Mode     string
	Items    map[string]vault.Item // resolved secretRef fields of the profile
	StateDir string                // persistent per-profile state (node keys of Tailscale/NetBird), 0700
	RunDir   string                // runtime files (generated configs, management passwords), removed on close
	Elevate  []string              // command prefix for privileged subprocesses in system mode ("sudo -n")
	Log      *Log
	// Browser is the configured browser for single sign-on ("" = detect).
	Browser string
	// Auth is called when the user has to log in through a browser.
	Auth func(AuthPrompt)
	// Code asks the user for a one-time code the gateway sent (SMS, email,
	// token) and waits for it; nil = codes cannot be asked for.
	Code func(ctx context.Context, p CodePrompt) (string, error)
	// Session keeps the gateway session between connection attempts of the
	// profile (FortiGate password logins); nil = log in every time.
	Session *Session
}

// AuthPrompt tells the user how to finish a login: open URL in their
// browser, or (Window) complete it in the login window opsdeck opened.
type AuthPrompt struct {
	URL    string
	Window bool
}

func (c *Config) prompt(p AuthPrompt) {
	if c.Auth != nil {
		c.Auth(p)
	}
}

// Value returns a field of the vault item a secretRef field points to.
func (c *Config) Value(field, name string) string {
	it, ok := c.Items[field]
	if !ok {
		return ""
	}
	return it.Get(name)
}

// Tunnel is a running VPN connection.
type Tunnel interface {
	// Dial reaches an address through the VPN. System-mode tunnels dial
	// through the operating system's routes.
	Dial(ctx context.Context, network, addr string) (net.Conn, error)
	Info() Info
	// Done is closed when the tunnel stopped by itself; Err tells why.
	Done() <-chan struct{}
	Err() error
	Close() error
}

// Info is live information about a tunnel.
type Info struct {
	Address       string    `json:"address,omitempty"`   // this machine's address in the VPN
	Interface     string    `json:"interface,omitempty"` // system mode
	Server        string    `json:"server,omitempty"`
	Detail        string    `json:"detail,omitempty"`
	RxBytes       int64     `json:"rxBytes"`
	TxBytes       int64     `json:"txBytes"`
	LastHandshake time.Time `json:"lastHandshake,omitempty"`
}

// Driver starts tunnels of one kind.
type Driver struct {
	Kind  string
	Label string
	Modes []string // supported, first is the default
	// Needs lists the executables a mode requires.
	Needs func(cfg *workspace.Resource, mode string) []string
	Start func(ctx context.Context, cfg *Config) (Tunnel, error)
	// InProcessSystem means system mode runs inside opsdeck (no sudo
	// possible): opsdeck itself needs root or CAP_NET_ADMIN.
	InProcessSystem bool
}

var drivers = map[string]*Driver{}

func register(d *Driver) { drivers[d.Kind] = d }

// Get returns the driver of a resource kind.
func Get(kind string) (*Driver, bool) {
	d, ok := drivers[kind]
	return d, ok
}

// ModeOf returns the profile's mode, defaulting to the driver's first mode.
func ModeOf(r *workspace.Resource) string {
	d, ok := drivers[r.Kind]
	if !ok {
		return ""
	}
	m := r.Str("mode")
	for _, x := range d.Modes {
		if x == m {
			return m
		}
	}
	return d.Modes[0]
}

// DriverInfo tells the UI what can run on this machine.
type DriverInfo struct {
	Kind    string            `json:"kind"`
	Label   string            `json:"label"`
	Modes   []string          `json:"modes"`
	Missing map[string]string `json:"missing"` // mode -> what is missing ("" = ready)
}

// Drivers reports availability of every driver; elevate is the configured
// command prefix for system mode.
func Drivers(elevate []string) []DriverInfo {
	out := []DriverInfo{}
	for _, k := range []string{"WireGuard", "Tailscale", "NetBird", "OpenVPN", "OpenConnect", "OpenFortiVPN"} {
		d := drivers[k]
		di := DriverInfo{Kind: d.Kind, Label: d.Label, Modes: d.Modes, Missing: map[string]string{}}
		for _, m := range d.Modes {
			di.Missing[m] = Missing(d, nil, m, elevate)
		}
		out = append(out, di)
	}
	return out
}

// Missing explains what prevents a mode from running here ("" = nothing).
func Missing(d *Driver, r *workspace.Resource, mode string, elevate []string) string {
	var miss []string
	if d.Needs != nil {
		for _, bin := range d.Needs(r, mode) {
			if _, err := exec.LookPath(bin); err != nil {
				miss = append(miss, bin)
			}
		}
	}
	if len(miss) > 0 {
		return "install " + strings.Join(miss, ", ")
	}
	if mode == ModeSystem && !Privileged() {
		if d.InProcessSystem {
			return "opsdeck must run as root or with CAP_NET_ADMIN (e.g. sudo setcap cap_net_admin,cap_net_raw+ep $(which opsdeck))"
		}
		if len(elevate) == 0 {
			return "needs root: set \"Run system VPNs with\" to sudo or pkexec in App settings"
		}
		if _, err := exec.LookPath(elevate[0]); err != nil {
			return "install " + elevate[0]
		}
	}
	return ""
}

// Privileged reports whether opsdeck can configure network interfaces itself.
func Privileged() bool { return os.Geteuid() == 0 || hasNetAdmin() }

// Elevation returns the command prefix for a setting: "auto" (sudo when not
// root), "sudo", "pkexec" or "none".
func Elevation(setting string) []string {
	switch setting {
	case "none":
		return nil
	case "pkexec":
		return []string{"pkexec"}
	case "sudo":
		return []string{"sudo", "-n"}
	}
	if os.Geteuid() == 0 {
		return nil
	}
	return []string{"sudo", "-n"}
}

// ---------------------------------------------------------------------------

// Log keeps the last lines of a tunnel's log.
type Log struct {
	mu    sync.Mutex
	lines []LogLine
	part  []byte
}

type LogLine struct {
	Time time.Time `json:"time"`
	Text string    `json:"text"`
}

const maxLogLines = 1000

func NewLog() *Log { return &Log{} }

func (l *Log) Printf(format string, a ...any) { l.add(fmt.Sprintf(format, a...)) }

func (l *Log) add(s string) {
	s = strings.TrimRight(s, "\r\n")
	if s == "" {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, LogLine{time.Now(), s})
	if len(l.lines) > maxLogLines {
		l.lines = append([]LogLine(nil), l.lines[len(l.lines)-maxLogLines:]...)
	}
}

// Write makes Log an io.Writer (one entry per line).
func (l *Log) Write(p []byte) (int, error) {
	l.mu.Lock()
	buf := append(l.part, p...)
	l.part = nil
	var lines []string
	for {
		i := strings.IndexByte(string(buf), '\n')
		if i < 0 {
			break
		}
		lines = append(lines, string(buf[:i]))
		buf = buf[i+1:]
	}
	if len(buf) > 4096 {
		lines, buf = append(lines, string(buf)), nil
	}
	l.part = append([]byte(nil), buf...)
	l.mu.Unlock()
	for _, s := range lines {
		l.add(s)
	}
	return len(p), nil
}

var _ io.Writer = (*Log)(nil)

// Lines returns the last n lines (all when n <= 0).
func (l *Log) Lines(n int) []LogLine {
	l.mu.Lock()
	defer l.mu.Unlock()
	if n <= 0 || n > len(l.lines) {
		n = len(l.lines)
	}
	return append([]LogLine{}, l.lines[len(l.lines)-n:]...)
}

// Tail returns the last n lines as text.
func (l *Log) Tail(n int) string {
	var b strings.Builder
	for _, x := range l.Lines(n) {
		b.WriteString(x.Text + "\n")
	}
	return b.String()
}

// ---------------------------------------------------------------------------

// direct dials through the operating system (system-mode tunnels).
func direct(ctx context.Context, network, addr string) (net.Conn, error) {
	var d net.Dialer
	return d.DialContext(ctx, network, addr)
}

// doneOnce is a Done channel with the reason it closed.
type doneOnce struct {
	once sync.Once
	ch   chan struct{}
	mu   sync.Mutex
	err  error
}

func newDone() *doneOnce { return &doneOnce{ch: make(chan struct{})} }

func (d *doneOnce) finish(err error) {
	d.once.Do(func() {
		d.mu.Lock()
		d.err = err
		d.mu.Unlock()
		close(d.ch)
	})
}

func (d *doneOnce) Done() <-chan struct{} { return d.ch }

func (d *doneOnce) Err() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.err
}

// ErrStopped is the error of a tunnel closed on request.
var ErrStopped = errors.New("disconnected")

// freePort returns a free TCP port on 127.0.0.1.
func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

// ifaceName derives a system interface name (max 15 chars) from a profile.
func ifaceName(prefix, name string) string {
	var b strings.Builder
	for _, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			b.WriteRune(r)
		}
	}
	s := prefix + b.String()
	if len(s) > 15 {
		s = s[:15]
	}
	return s
}

func writePrivate(path, content string) error {
	return os.WriteFile(path, []byte(content), 0o600)
}
