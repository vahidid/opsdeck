package vpn

import (
	"context"
	"crypto/subtle"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"sync"
	"time"
)

// SocksServer exposes a userspace tunnel to other apps on this machine as a
// SOCKS5 proxy on 127.0.0.1 (CONNECT only, no authentication: it is bound to
// loopback). Host names are resolved through the tunnel.
type SocksServer struct {
	ln         net.Listener
	dial       DialFunc
	wg         sync.WaitGroup
	user, pass string // when set, clients must authenticate (RFC 1929)
}

// ServeSOCKS listens on 127.0.0.1:port.
func ServeSOCKS(port int, dial DialFunc) (*SocksServer, error) {
	return ServeSOCKSAuth(port, dial, "", "")
}

// ServeSOCKSAuth listens on 127.0.0.1:port (0 = any free port) and, when
// user is set, only serves clients that present user and pass.
func ServeSOCKSAuth(port int, dial DialFunc, user, pass string) (*SocksServer, error) {
	ln, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		return nil, fmt.Errorf("local proxy on port %d: %w", port, err)
	}
	s := &SocksServer{ln: ln, dial: dial, user: user, pass: pass}
	s.wg.Add(1)
	go s.serve()
	return s, nil
}

// Addr is the proxy's listen address.
func (s *SocksServer) Addr() string { return s.ln.Addr().String() }

func (s *SocksServer) Close() error {
	err := s.ln.Close()
	s.wg.Wait()
	return err
}

func (s *SocksServer) serve() {
	defer s.wg.Done()
	for {
		c, err := s.ln.Accept()
		if err != nil {
			return
		}
		go s.handle(c)
	}
}

func (s *SocksServer) handle(c net.Conn) {
	defer c.Close()
	c.SetDeadline(time.Now().Add(30 * time.Second))
	target, err := socksHandshake(c, s.user, s.pass)
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	up, err := s.dial(ctx, "tcp", target)
	cancel()
	if err != nil {
		c.Write([]byte{5, 4, 0, 1, 0, 0, 0, 0, 0, 0}) // host unreachable
		return
	}
	defer up.Close()
	if _, err := c.Write([]byte{5, 0, 0, 1, 0, 0, 0, 0, 0, 0}); err != nil {
		return
	}
	c.SetDeadline(time.Time{})
	done := make(chan struct{}, 2)
	go func() { io.Copy(up, c); closeWrite(up); done <- struct{}{} }()
	go func() { io.Copy(c, up); closeWrite(c); done <- struct{}{} }()
	<-done
	<-done
}

func closeWrite(c net.Conn) {
	if cw, ok := c.(interface{ CloseWrite() error }); ok {
		cw.CloseWrite()
	} else {
		c.Close()
	}
}

// socksHandshake reads the greeting (and credentials when required) and a
// CONNECT request, returning host:port.
func socksHandshake(c net.Conn, user, pass string) (string, error) {
	var h [2]byte
	if _, err := io.ReadFull(c, h[:]); err != nil || h[0] != 5 {
		return "", errors.New("not socks5")
	}
	methods := make([]byte, h[1])
	if _, err := io.ReadFull(c, methods); err != nil {
		return "", err
	}
	want := byte(0)
	if user != "" {
		want = 2
	}
	ok := false
	for _, m := range methods {
		if m == want {
			ok = true
		}
	}
	if !ok {
		c.Write([]byte{5, 0xff})
		return "", errors.New("no acceptable auth method")
	}
	if _, err := c.Write([]byte{5, want}); err != nil {
		return "", err
	}
	if want == 2 {
		var v [2]byte
		if _, err := io.ReadFull(c, v[:]); err != nil || v[0] != 1 {
			return "", errors.New("bad auth request")
		}
		u := make([]byte, v[1])
		if _, err := io.ReadFull(c, u); err != nil {
			return "", err
		}
		var pl [1]byte
		if _, err := io.ReadFull(c, pl[:]); err != nil {
			return "", err
		}
		p := make([]byte, pl[0])
		if _, err := io.ReadFull(c, p); err != nil {
			return "", err
		}
		if subtle.ConstantTimeCompare(u, []byte(user)) != 1 || subtle.ConstantTimeCompare(p, []byte(pass)) != 1 {
			c.Write([]byte{1, 1})
			return "", errors.New("bad credentials")
		}
		if _, err := c.Write([]byte{1, 0}); err != nil {
			return "", err
		}
	}
	var req [4]byte
	if _, err := io.ReadFull(c, req[:]); err != nil {
		return "", err
	}
	if req[0] != 5 || req[1] != 1 { // only CONNECT
		c.Write([]byte{5, 7, 0, 1, 0, 0, 0, 0, 0, 0})
		return "", errors.New("unsupported command")
	}
	var host string
	switch req[3] {
	case 1:
		b := make([]byte, 4)
		if _, err := io.ReadFull(c, b); err != nil {
			return "", err
		}
		host = net.IP(b).String()
	case 4:
		b := make([]byte, 16)
		if _, err := io.ReadFull(c, b); err != nil {
			return "", err
		}
		host = net.IP(b).String()
	case 3:
		var n [1]byte
		if _, err := io.ReadFull(c, n[:]); err != nil {
			return "", err
		}
		b := make([]byte, n[0])
		if _, err := io.ReadFull(c, b); err != nil {
			return "", err
		}
		host = string(b)
	default:
		return "", errors.New("bad address type")
	}
	var p [2]byte
	if _, err := io.ReadFull(c, p[:]); err != nil {
		return "", err
	}
	return net.JoinHostPort(host, strconv.Itoa(int(binary.BigEndian.Uint16(p[:])))), nil
}
