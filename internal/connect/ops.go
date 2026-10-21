package connect

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"

	"opsdeck/internal/workspace"
)

// ExecResult is the outcome of a remote command.
type ExecResult struct {
	Stdout     string `json:"stdout"`
	Stderr     string `json:"stderr"`
	ExitCode   int    `json:"exitCode"`
	DurationMs int64  `json:"durationMs"`
	Truncated  bool   `json:"truncated,omitempty"`
}

// limitedBuffer keeps at most max bytes (head), counting the rest.
type limitedBuffer struct {
	buf       bytes.Buffer
	max       int
	truncated bool
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if room := b.max - b.buf.Len(); room > 0 {
		if len(p) > room {
			b.buf.Write(p[:room])
			b.truncated = true
		} else {
			b.buf.Write(p)
		}
	} else if len(p) > 0 {
		b.truncated = true
	}
	return len(p), nil
}

// Exec runs a command on a host. stdin may be nil.
func (h *Hub) Exec(ctx context.Context, w *workspace.Workspace, host *workspace.Resource, cmd string, stdin io.Reader, maxOutput int) (ExecResult, error) {
	if maxOutput <= 0 {
		maxOutput = 1 << 20
	}
	client, err := h.SSH(ctx, w, host)
	if err != nil {
		return ExecResult{}, err
	}
	sess, err := client.NewSession()
	if err != nil {
		// The pooled connection may be dead; retry once with a fresh one.
		h.Disconnect(w.Name(), host.Name)
		if client, err = h.SSH(ctx, w, host); err != nil {
			return ExecResult{}, err
		}
		if sess, err = client.NewSession(); err != nil {
			return ExecResult{}, err
		}
	}
	defer sess.Close()
	stdout := &limitedBuffer{max: maxOutput}
	stderr := &limitedBuffer{max: maxOutput / 4}
	sess.Stdout, sess.Stderr = stdout, stderr
	if stdin != nil {
		sess.Stdin = stdin
	}
	start := time.Now()
	if err := sess.Start(cmd); err != nil {
		return ExecResult{}, err
	}
	done := make(chan error, 1)
	go func() { done <- sess.Wait() }()
	var waitErr error
	select {
	case waitErr = <-done:
	case <-ctx.Done():
		sess.Signal(ssh.SIGKILL)
		sess.Close()
		<-done
		return ExecResult{Stdout: stdout.buf.String(), Stderr: stderr.buf.String(), ExitCode: -1,
			DurationMs: time.Since(start).Milliseconds()}, fmt.Errorf("command interrupted: %w", ctx.Err())
	}
	res := ExecResult{Stdout: stdout.buf.String(), Stderr: stderr.buf.String(), DurationMs: time.Since(start).Milliseconds(),
		Truncated: stdout.truncated || stderr.truncated}
	var exitErr *ssh.ExitError
	var missing *ssh.ExitMissingError
	switch {
	case waitErr == nil:
	case errors.As(waitErr, &exitErr):
		res.ExitCode = exitErr.ExitStatus()
	case errors.As(waitErr, &missing):
		res.ExitCode = -1
	default:
		return res, waitErr
	}
	return res, nil
}

// ExecStream runs a command and calls onLine for every output line until it
// exits or ctx is cancelled.
func (h *Hub) ExecStream(ctx context.Context, w *workspace.Workspace, host *workspace.Resource, cmd string, onLine func(string)) error {
	client, err := h.SSH(ctx, w, host)
	if err != nil {
		return err
	}
	sess, err := client.NewSession()
	if err != nil {
		return err
	}
	defer sess.Close()
	pr, pw := io.Pipe()
	sess.Stdout, sess.Stderr = pw, pw
	if err := sess.Start(cmd); err != nil {
		return err
	}
	go func() {
		err := sess.Wait()
		pw.CloseWithError(err)
	}()
	go func() {
		<-ctx.Done()
		sess.Signal(ssh.SIGTERM)
		sess.Close()
		pw.Close()
	}()
	sc := bufio.NewScanner(pr)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		onLine(sc.Text())
	}
	if ctx.Err() != nil {
		return nil
	}
	var exitErr *ssh.ExitError
	if err := sc.Err(); err != nil && !errors.As(err, &exitErr) && !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}

// Shell is an interactive PTY session.
type Shell struct {
	sess   *ssh.Session
	Stdin  io.WriteCloser
	Stdout io.Reader
}

func (s *Shell) Resize(cols, rows int) error { return s.sess.WindowChange(rows, cols) }
func (s *Shell) Wait() error                 { return s.sess.Wait() }
func (s *Shell) Close() error                { return s.sess.Close() }

// OpenShell starts a login shell with a PTY.
func (h *Hub) OpenShell(ctx context.Context, w *workspace.Workspace, host *workspace.Resource, cols, rows int) (*Shell, error) {
	client, err := h.SSH(ctx, w, host)
	if err != nil {
		return nil, err
	}
	sess, err := client.NewSession()
	if err != nil {
		return nil, err
	}
	modes := ssh.TerminalModes{ssh.ECHO: 1, ssh.TTY_OP_ISPEED: 14400, ssh.TTY_OP_OSPEED: 14400}
	if cols <= 0 {
		cols = 120
	}
	if rows <= 0 {
		rows = 32
	}
	if err := sess.RequestPty("xterm-256color", rows, cols, modes); err != nil {
		sess.Close()
		return nil, err
	}
	stdin, err := sess.StdinPipe()
	if err != nil {
		sess.Close()
		return nil, err
	}
	pr, pw := io.Pipe()
	sess.Stdout, sess.Stderr = pw, pw
	if err := sess.Shell(); err != nil {
		sess.Close()
		return nil, err
	}
	go func() { pw.CloseWithError(sess.Wait()) }()
	return &Shell{sess: sess, Stdin: stdin, Stdout: pr}, nil
}

// SFTP opens an SFTP client (caller closes it).
func (h *Hub) SFTP(ctx context.Context, w *workspace.Workspace, host *workspace.Resource) (*sftp.Client, error) {
	client, err := h.SSH(ctx, w, host)
	if err != nil {
		return nil, err
	}
	return sftp.NewClient(client)
}

// FileEntry is a directory listing entry.
type FileEntry struct {
	Name     string    `json:"name"`
	Dir      bool      `json:"dir"`
	Size     int64     `json:"size"`
	Mode     string    `json:"mode"`
	Modified time.Time `json:"modified"`
	Link     bool      `json:"link,omitempty"`
}

func (h *Hub) ListDir(ctx context.Context, w *workspace.Workspace, host *workspace.Resource, path string) (string, []FileEntry, error) {
	c, err := h.SFTP(ctx, w, host)
	if err != nil {
		return "", nil, err
	}
	defer c.Close()
	if path == "" || path == "~" {
		if path, err = c.Getwd(); err != nil {
			path = "/"
		}
	}
	infos, err := c.ReadDir(path)
	if err != nil {
		return path, nil, err
	}
	out := make([]FileEntry, 0, len(infos))
	for _, fi := range infos {
		e := FileEntry{Name: fi.Name(), Dir: fi.IsDir(), Size: fi.Size(), Mode: fi.Mode().String(), Modified: fi.ModTime()}
		if fi.Mode()&os.ModeSymlink != 0 {
			e.Link = true
			if st, err := c.Stat(joinPath(path, fi.Name())); err == nil {
				e.Dir = st.IsDir()
			}
		}
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Dir != out[j].Dir {
			return out[i].Dir
		}
		return out[i].Name < out[j].Name
	})
	return path, out, nil
}

func joinPath(dir, name string) string {
	if strings.HasSuffix(dir, "/") {
		return dir + name
	}
	return dir + "/" + name
}

// ReadFile reads up to max bytes of a remote file.
func (h *Hub) ReadFile(ctx context.Context, w *workspace.Workspace, host *workspace.Resource, path string, max int64) (string, bool, error) {
	c, err := h.SFTP(ctx, w, host)
	if err != nil {
		return "", false, err
	}
	defer c.Close()
	f, err := c.Open(path)
	if err != nil {
		return "", false, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil {
		return "", false, err
	}
	if int64(len(b)) > max {
		return string(b[:max]), true, nil
	}
	return string(b), false, nil
}

// WriteFile writes a remote file atomically (temp file + rename), keeping the
// original mode and owner. The temp file has a random name, is created
// exclusively (no symlink following) and gets its final mode before any
// content is written.
func (h *Hub) WriteFile(ctx context.Context, w *workspace.Workspace, host *workspace.Resource, path, content string) error {
	c, err := h.SFTP(ctx, w, host)
	if err != nil {
		return err
	}
	defer c.Close()
	mode := os.FileMode(0o644)
	uid, gid, haveOwner := 0, 0, false
	if st, err := c.Stat(path); err == nil {
		mode = st.Mode().Perm()
		if fs, ok := st.Sys().(*sftp.FileStat); ok {
			uid, gid, haveOwner = int(fs.UID), int(fs.GID), true
		}
	}
	suffix := make([]byte, 8)
	rand.Read(suffix)
	tmp := fmt.Sprintf("%s.opsdeck-%x", path, suffix)
	f, err := c.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL)
	if err != nil {
		return err
	}
	cleanup := func() { f.Close(); c.Remove(tmp) }
	if err := f.Chmod(0o600); err != nil {
		cleanup()
		return err
	}
	if _, err := f.Write([]byte(content)); err != nil {
		cleanup()
		return err
	}
	f.Close()
	if haveOwner {
		c.Chown(tmp, uid, gid) // best effort: only root can give files away
	}
	if err := c.Chmod(tmp, mode); err != nil {
		c.Remove(tmp)
		return err
	}
	if err := c.PosixRename(tmp, path); err != nil {
		if err2 := c.Rename(tmp, path); err2 != nil {
			c.Remove(tmp)
			return err
		}
	}
	return nil
}

// Check measures reachability of a host's SSH port through its path.
type CheckResult struct {
	Reachable bool     `json:"reachable"`
	LatencyMs int64    `json:"latencyMs"`
	Error     string   `json:"error,omitempty"`
	Path      []string `json:"path"`
	SSH       bool     `json:"ssh"`
}

func (h *Hub) Check(ctx context.Context, w *workspace.Workspace, host *workspace.Resource) CheckResult {
	res := CheckResult{}
	res.Path, _ = DescribePath(w, host)
	dial, err := h.Dialer(ctx, w, host)
	if err != nil {
		res.Error = err.Error()
		return res
	}
	start := time.Now()
	dctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	conn, err := dial(dctx, "tcp", hostAddr(host))
	if err != nil {
		res.Error = err.Error()
		return res
	}
	res.LatencyMs = time.Since(start).Milliseconds()
	res.Reachable = true
	conn.Close()
	if _, err := h.SSH(ctx, w, host); err != nil {
		res.Error = err.Error()
	} else {
		res.SSH = true
	}
	return res
}

// TCPCheck dials addr from the vantage point of r's path (or locally).
func (h *Hub) TCPCheck(ctx context.Context, w *workspace.Workspace, from *workspace.Resource, addr string) (time.Duration, error) {
	var dial DialFunc
	if from == nil {
		var d net.Dialer
		dial = d.DialContext
	} else if from.Kind == "Host" {
		c, err := h.SSH(ctx, w, from)
		if err != nil {
			return 0, err
		}
		dial = c.DialContext
	} else {
		var err error
		if dial, err = h.Dialer(ctx, w, from); err != nil {
			return 0, err
		}
	}
	start := time.Now()
	dctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	conn, err := dial(dctx, "tcp", addr)
	if err != nil {
		return 0, err
	}
	conn.Close()
	return time.Since(start), nil
}

// HostStats is a quick agentless snapshot of a Linux host.
type HostStats struct {
	Hostname  string     `json:"hostname"`
	OS        string     `json:"os"`
	Kernel    string     `json:"kernel"`
	UptimeSec int64      `json:"uptimeSec"`
	Load      []float64  `json:"load"`
	CPUs      int        `json:"cpus"`
	MemTotal  int64      `json:"memTotal"`
	MemUsed   int64      `json:"memUsed"`
	SwapTotal int64      `json:"swapTotal"`
	SwapUsed  int64      `json:"swapUsed"`
	Disks     []DiskStat `json:"disks"`
}

type DiskStat struct {
	Mount string `json:"mount"`
	Size  int64  `json:"size"`
	Used  int64  `json:"used"`
}

const statsScript = `hostname; echo ---; (. /etc/os-release 2>/dev/null && echo "$PRETTY_NAME") || uname -s; echo ---; uname -r; echo ---; cat /proc/uptime; echo ---; cat /proc/loadavg; echo ---; nproc 2>/dev/null || grep -c ^processor /proc/cpuinfo; echo ---; cat /proc/meminfo; echo ---; df -PB1 -x tmpfs -x devtmpfs -x overlay -x squashfs 2>/dev/null || df -Pk`

func (h *Hub) Stats(ctx context.Context, w *workspace.Workspace, host *workspace.Resource) (HostStats, error) {
	res, err := h.Exec(ctx, w, host, statsScript, nil, 256*1024)
	if err != nil {
		return HostStats{}, err
	}
	parts := strings.Split(res.Stdout, "---\n")
	if len(parts) < 8 {
		return HostStats{}, fmt.Errorf("unexpected output (is this a Linux host?): %s", strings.TrimSpace(res.Stderr))
	}
	s := HostStats{
		Hostname: strings.TrimSpace(parts[0]), OS: strings.TrimSpace(parts[1]), Kernel: strings.TrimSpace(parts[2]),
		Load: []float64{}, Disks: []DiskStat{},
	}
	if f := strings.Fields(parts[3]); len(f) > 0 {
		v, _ := strconv.ParseFloat(f[0], 64)
		s.UptimeSec = int64(v)
	}
	for i, f := range strings.Fields(parts[4]) {
		if i < 3 {
			v, _ := strconv.ParseFloat(f, 64)
			s.Load = append(s.Load, v)
		}
	}
	s.CPUs, _ = strconv.Atoi(strings.TrimSpace(parts[5]))
	mem := map[string]int64{}
	for _, line := range strings.Split(parts[6], "\n") {
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		f := strings.Fields(v)
		if len(f) > 0 {
			n, _ := strconv.ParseInt(f[0], 10, 64)
			mem[k] = n * 1024
		}
	}
	s.MemTotal = mem["MemTotal"]
	s.MemUsed = mem["MemTotal"] - mem["MemAvailable"]
	s.SwapTotal = mem["SwapTotal"]
	s.SwapUsed = mem["SwapTotal"] - mem["SwapFree"]
	blockSize := int64(1)
	lines := strings.Split(strings.TrimSpace(parts[7]), "\n")
	if len(lines) > 0 && strings.Contains(lines[0], "1024-blocks") {
		blockSize = 1024
	}
	for _, line := range lines[1:] {
		f := strings.Fields(line)
		if len(f) < 6 {
			continue
		}
		size, _ := strconv.ParseInt(f[1], 10, 64)
		used, _ := strconv.ParseInt(f[2], 10, 64)
		if size == 0 {
			continue
		}
		s.Disks = append(s.Disks, DiskStat{Mount: f[5], Size: size * blockSize, Used: used * blockSize})
	}
	return s, nil
}
