// Package wgpeer runs a WireGuard "office" peer in-process for tests: a
// userspace WireGuard server at 10.9.0.2 serving HTTP on port 8080, plus the
// wg-quick config a client needs to reach it.
package wgpeer

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"net/netip"

	"golang.org/x/crypto/curve25519"
	"golang.zx2c4.com/wireguard/conn"
	"golang.zx2c4.com/wireguard/device"
	"golang.zx2c4.com/wireguard/tun/netstack"
)

// Peer is a running WireGuard server.
type Peer struct {
	ClientConfig string // wg-quick config for 10.9.0.1
	dev          *device.Device
	srv          *http.Server
}

// Body is what the peer's web server answers.
const Body = "hello from the office network"

func keys() (priv, pub []byte) {
	priv = make([]byte, 32)
	rand.Read(priv)
	priv[0] &= 248
	priv[31] = (priv[31] & 127) | 64
	pub, _ = curve25519.X25519(priv, curve25519.Basepoint)
	return
}

func Start() (*Peer, error) {
	return StartWith(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, Body) }))
}

// StartWith serves h on 10.9.0.2:8080 instead of the greeting.
func StartWith(h http.Handler) (*Peer, error) {
	cPriv, cPub := keys()
	sPriv, sPub := keys()
	udp, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	port := udp.LocalAddr().(*net.UDPAddr).Port
	udp.Close()
	tun, tnet, err := netstack.CreateNetTUN([]netip.Addr{netip.MustParseAddr("10.9.0.2")}, nil, 1420)
	if err != nil {
		return nil, err
	}
	dev := device.NewDevice(tun, conn.NewDefaultBind(), &device.Logger{Verbosef: device.DiscardLogf, Errorf: device.DiscardLogf})
	if err := dev.IpcSet(fmt.Sprintf("private_key=%s\nlisten_port=%d\npublic_key=%s\nallowed_ip=10.9.0.1/32\n",
		hex.EncodeToString(sPriv), port, hex.EncodeToString(cPub))); err != nil {
		dev.Close()
		return nil, err
	}
	dev.Up()
	ln, err := tnet.ListenTCP(&net.TCPAddr{IP: net.ParseIP("10.9.0.2"), Port: 8080})
	if err != nil {
		dev.Close()
		return nil, err
	}
	srv := &http.Server{Handler: h}
	go srv.Serve(ln)
	conf := fmt.Sprintf("[Interface]\nPrivateKey = %s\nAddress = 10.9.0.1/32\n\n[Peer]\nPublicKey = %s\nEndpoint = 127.0.0.1:%d\nAllowedIPs = 10.9.0.0/24\nPersistentKeepalive = 5\n",
		base64.StdEncoding.EncodeToString(cPriv), base64.StdEncoding.EncodeToString(sPub), port)
	return &Peer{ClientConfig: conf, dev: dev, srv: srv}, nil
}

func (p *Peer) Close() {
	p.srv.Close()
	p.dev.Close()
}
