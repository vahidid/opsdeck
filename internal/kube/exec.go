package kube

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/httpstream"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/portforward"
	"k8s.io/client-go/tools/remotecommand"
	"k8s.io/client-go/transport/spdy"
	utilexec "k8s.io/client-go/util/exec"
)

// AutoShell starts bash when the image has it, else sh.
var AutoShell = []string{"sh", "-c", "export TERM=xterm-256color; if command -v bash >/dev/null 2>&1; then exec bash; else exec sh; fi"}

func fallbackOK(err error) bool {
	return httpstream.IsUpgradeFailure(err) || httpstream.IsHTTPSProxyError(err)
}

func (c *Conn) executor(ns, pod, container string, cmd []string, tty, stdin bool) (remotecommand.Executor, error) {
	req := c.CS.CoreV1().RESTClient().Post().Resource("pods").Namespace(ns).Name(pod).SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{Container: container, Command: cmd, Stdin: stdin, Stdout: true, Stderr: !tty, TTY: tty}, scheme.ParameterCodec)
	ws, err := remotecommand.NewWebSocketExecutor(c.RC, "GET", req.URL().String())
	if err != nil {
		return nil, err
	}
	sp, err := remotecommand.NewSPDYExecutor(c.RC, "POST", req.URL())
	if err != nil {
		return nil, err
	}
	return remotecommand.NewFallbackExecutor(ws, sp, fallbackOK)
}

// Exec runs a command in a container without a terminal and returns its
// output and exit code.
func (c *Conn) Exec(ctx context.Context, ns, pod, container string, cmd []string, stdin io.Reader, max int) (stdout, stderr string, code int, err error) {
	ex, err := c.executor(ns, pod, container, cmd, false, stdin != nil)
	if err != nil {
		return "", "", -1, err
	}
	var o, e limited
	o.max, e.max = max, max/4
	err = ex.StreamWithContext(ctx, remotecommand.StreamOptions{Stdin: stdin, Stdout: &o, Stderr: &e})
	var ce utilexec.CodeExitError
	if errors.As(err, &ce) {
		return o.String(), e.String(), ce.ExitStatus(), nil
	}
	if err != nil {
		return o.String(), e.String(), -1, err
	}
	return o.String(), e.String(), 0, nil
}

type limited struct {
	buf bytes.Buffer
	max int
	cut bool
}

func (l *limited) Write(p []byte) (int, error) {
	if room := l.max - l.buf.Len(); room > 0 {
		if len(p) > room {
			l.buf.Write(p[:room])
			l.cut = true
		} else {
			l.buf.Write(p)
		}
	} else {
		l.cut = true
	}
	return len(p), nil
}

func (l *limited) String() string {
	if l.cut {
		return l.buf.String() + "\n… (output truncated)"
	}
	return l.buf.String()
}

// Terminal is an interactive exec session with a TTY.
type Terminal struct {
	in     *io.PipeWriter
	out    *io.PipeReader
	sizes  chan remotecommand.TerminalSize
	done   chan error
	cancel context.CancelFunc
	once   sync.Once
}

func (t *Terminal) Read(p []byte) (int, error)  { return t.out.Read(p) }
func (t *Terminal) Write(p []byte) (int, error) { return t.in.Write(p) }
func (t *Terminal) Wait() error                 { return <-t.done }
func (t *Terminal) Resize(cols, rows int) error {
	select {
	case t.sizes <- remotecommand.TerminalSize{Width: uint16(cols), Height: uint16(rows)}:
	default:
	}
	return nil
}
func (t *Terminal) Close() error {
	t.once.Do(func() {
		t.cancel()
		t.in.Close()
	})
	return nil
}

type sizeQueue chan remotecommand.TerminalSize

func (q sizeQueue) Next() *remotecommand.TerminalSize {
	s, ok := <-q
	if !ok {
		return nil
	}
	return &s
}

// Shell opens an interactive terminal in a container (cmd nil = AutoShell).
func (c *Conn) Shell(ns, pod, container string, cmd []string, cols, rows int) (*Terminal, error) {
	if len(cmd) == 0 {
		cmd = AutoShell
	}
	ex, err := c.executor(ns, pod, container, cmd, true, true)
	if err != nil {
		return nil, err
	}
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	t := &Terminal{in: inW, out: outR, sizes: make(chan remotecommand.TerminalSize, 4), done: make(chan error, 1), cancel: cancel}
	if cols > 0 && rows > 0 {
		t.sizes <- remotecommand.TerminalSize{Width: uint16(cols), Height: uint16(rows)}
	}
	go func() {
		err := ex.StreamWithContext(ctx, remotecommand.StreamOptions{Stdin: inR, Stdout: outW, Tty: true, TerminalSizeQueue: sizeQueue(t.sizes)})
		var ce utilexec.CodeExitError
		if errors.As(err, &ce) || errors.Is(err, context.Canceled) {
			err = nil
		}
		if err != nil {
			fmt.Fprintf(outW, "\r\n\x1b[31m✖ %v\x1b[0m\r\n", err)
		}
		outW.Close()
		inR.Close()
		t.done <- err
	}()
	return t, nil
}

// ---------------------------------------------------------------------------
// Port forwarding

// ForwardTarget resolves where a port-forward to an object goes: the pod
// itself, or for a service or workload a running pod behind it, with the
// service port mapped to the container port like kubectl does.
func (c *Conn) ForwardTarget(ctx context.Context, rt ResourceType, ns, name string, port int) (pod string, podPort int, err error) {
	switch rt.ID {
	case "pods":
		return name, port, nil
	case "services":
		svc, err := c.CS.CoreV1().Services(ns).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return "", 0, err
		}
		var sp *corev1.ServicePort
		for i := range svc.Spec.Ports {
			if int(svc.Spec.Ports[i].Port) == port {
				sp = &svc.Spec.Ports[i]
			}
		}
		if sp == nil {
			return "", 0, fmt.Errorf("service %s has no port %d", name, port)
		}
		pods, err := c.PodsFor(ctx, rt, ns, name)
		if err != nil {
			return "", 0, err
		}
		p, err := runningPod(pods)
		if err != nil {
			return "", 0, err
		}
		target := sp.TargetPort
		if target.Type == intstr.Int {
			if target.IntValue() == 0 {
				return p.Name, port, nil
			}
			return p.Name, target.IntValue(), nil
		}
		for _, ct := range p.Spec.Containers {
			for _, cp := range ct.Ports {
				if cp.Name == target.StrVal {
					return p.Name, int(cp.ContainerPort), nil
				}
			}
		}
		return "", 0, fmt.Errorf("no container port named %q in pod %s", target.StrVal, p.Name)
	}
	pods, err := c.PodsFor(ctx, rt, ns, name)
	if err != nil {
		return "", 0, err
	}
	p, err := runningPod(pods)
	if err != nil {
		return "", 0, err
	}
	return p.Name, port, nil
}

func runningPod(pods []corev1.Pod) (*corev1.Pod, error) {
	for i := range pods {
		p := &pods[i]
		if p.Status.Phase != corev1.PodRunning || p.DeletionTimestamp != nil {
			continue
		}
		for _, cond := range p.Status.Conditions {
			if cond.Type == corev1.PodReady && cond.Status == corev1.ConditionTrue {
				return p, nil
			}
		}
	}
	for i := range pods {
		if pods[i].Status.Phase == corev1.PodRunning {
			return &pods[i], nil
		}
	}
	return nil, errors.New("no running pod")
}

// DialPod opens one connection to a pod's port (the API server's
// port-forward), usable as a net.Conn.
func (c *Conn) DialPod(ctx context.Context, ns, pod string, port int) (net.Conn, error) {
	u := c.CS.CoreV1().RESTClient().Post().Resource("pods").Namespace(ns).Name(pod).SubResource("portforward").URL()
	transport, upgrader, err := spdy.RoundTripperFor(c.RC)
	if err != nil {
		return nil, err
	}
	var dialer httpstream.Dialer = spdy.NewDialer(upgrader, &http.Client{Transport: transport}, "POST", u)
	if ws, err := portforward.NewSPDYOverWebsocketDialer(u, c.RC); err == nil {
		dialer = portforward.NewFallbackDialer(ws, dialer, fallbackOK)
	}
	conn, _, err := dialer.Dial(portforward.PortForwardProtocolV1Name)
	if err != nil {
		return nil, err
	}
	h := http.Header{}
	h.Set(corev1.StreamType, corev1.StreamTypeError)
	h.Set(corev1.PortHeader, strconv.Itoa(port))
	h.Set(corev1.PortForwardRequestIDHeader, "0")
	errStream, err := conn.CreateStream(h)
	if err != nil {
		conn.Close()
		return nil, err
	}
	errStream.Close()
	h.Set(corev1.StreamType, corev1.StreamTypeData)
	data, err := conn.CreateStream(h)
	if err != nil {
		conn.Close()
		return nil, err
	}
	pc := &podConn{conn: conn, data: data, addr: pod + ":" + strconv.Itoa(port)}
	go func() {
		msg, _ := io.ReadAll(errStream)
		if s := strings.TrimSpace(string(msg)); s != "" {
			pc.fail(errors.New(s))
		}
	}()
	return pc, nil
}

type podConn struct {
	conn httpstream.Connection
	data httpstream.Stream
	addr string
	mu   sync.Mutex
	err  error
	once sync.Once
}

func (p *podConn) fail(err error) {
	p.mu.Lock()
	p.err = err
	p.mu.Unlock()
	p.Close()
}

func (p *podConn) Read(b []byte) (int, error) {
	n, err := p.data.Read(b)
	if err != nil {
		p.mu.Lock()
		if p.err != nil {
			err = p.err
		}
		p.mu.Unlock()
	}
	return n, err
}
func (p *podConn) Write(b []byte) (int, error) { return p.data.Write(b) }
func (p *podConn) Close() error {
	p.once.Do(func() {
		p.data.Close()
		p.conn.RemoveStreams(p.data)
		p.conn.Close()
	})
	return nil
}
func (p *podConn) LocalAddr() net.Addr              { return addr("opsdeck") }
func (p *podConn) RemoteAddr() net.Addr             { return addr(p.addr) }
func (p *podConn) SetDeadline(time.Time) error      { return nil }
func (p *podConn) SetReadDeadline(time.Time) error  { return nil }
func (p *podConn) SetWriteDeadline(time.Time) error { return nil }

type addr string

func (a addr) Network() string { return "kube" }
func (a addr) String() string  { return string(a) }
