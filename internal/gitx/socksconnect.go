package gitx

import (
	"errors"
	"io"
	"net"
	"os"
	"strings"

	"golang.org/x/net/proxy"
)

// SocksConnect implements `opsdeck socks-connect <proxy> <host> <port>`,
// ssh's ProxyCommand for SSH remotes behind a VPN or jump host: it joins
// stdin/stdout to host:port through opsdeck's SOCKS5 proxy, authenticating
// with OPSDECK_SOCKS_AUTH (user:password).
func SocksConnect(args []string, stdin io.Reader, stdout io.Writer) error {
	if len(args) != 3 {
		return errors.New("usage: opsdeck socks-connect <proxy host:port> <host> <port>")
	}
	var auth *proxy.Auth
	if u, p, ok := strings.Cut(os.Getenv("OPSDECK_SOCKS_AUTH"), ":"); ok {
		auth = &proxy.Auth{User: u, Password: p}
	}
	d, err := proxy.SOCKS5("tcp", args[0], auth, proxy.Direct)
	if err != nil {
		return err
	}
	c, err := d.Dial("tcp", net.JoinHostPort(args[1], args[2]))
	if err != nil {
		return err
	}
	defer c.Close()
	done := make(chan struct{})
	go func() {
		io.Copy(c, stdin)
		if cw, ok := c.(interface{ CloseWrite() error }); ok {
			cw.CloseWrite()
		}
	}()
	go func() { io.Copy(stdout, c); close(done) }()
	<-done
	return nil
}
