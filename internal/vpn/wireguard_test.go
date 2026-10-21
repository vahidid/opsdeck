package vpn

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/netip"
	"testing"
	"time"

	"golang.org/x/crypto/curve25519"
	"golang.zx2c4.com/wireguard/conn"
	"golang.zx2c4.com/wireguard/device"
	"golang.zx2c4.com/wireguard/tun/netstack"
)

func wgKeys(t *testing.T) (priv, pub []byte) {
	priv = make([]byte, 32)
	rand.Read(priv)
	priv[0] &= 248
	priv[31] = (priv[31] & 127) | 64
	pub, err := curve25519.X25519(priv, curve25519.Basepoint)
	if err != nil {
		t.Fatal(err)
	}
	return priv, pub
}

func TestWireGuardTunnel(t *testing.T) {
	aPriv, aPub := wgKeys(t)
	bPriv, bPub := wgKeys(t)

	// Peer B: the "office" side, with a TCP echo service on 10.9.0.2:2222.
	udp, _ := net.ListenPacket("udp", "127.0.0.1:0")
	bPort := udp.LocalAddr().(*net.UDPAddr).Port
	udp.Close()
	tunB, tnetB, err := netstack.CreateNetTUN([]netip.Addr{netip.MustParseAddr("10.9.0.2")}, nil, 1420)
	if err != nil {
		t.Fatal(err)
	}
	devB := device.NewDevice(tunB, conn.NewDefaultBind(), &device.Logger{Verbosef: device.DiscardLogf, Errorf: device.DiscardLogf})
	defer devB.Close()
	if err := devB.IpcSet(fmt.Sprintf("private_key=%s\nlisten_port=%d\npublic_key=%s\nallowed_ip=10.9.0.1/32\n",
		hex.EncodeToString(bPriv), bPort, hex.EncodeToString(aPub))); err != nil {
		t.Fatal(err)
	}
	devB.Up()
	ln, err := tnetB.ListenTCP(&net.TCPAddr{IP: net.ParseIP("10.9.0.2"), Port: 2222})
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() { io.Copy(c, c); c.Close() }()
		}
	}()

	// Peer A: opsdeck's tunnel from a wg-quick config.
	conf := fmt.Sprintf(`[Interface]
PrivateKey = %s
Address = 10.9.0.1/32

[Peer]
PublicKey = %s
Endpoint = 127.0.0.1:%d
AllowedIPs = 10.9.0.0/24
PersistentKeepalive = 5
`, base64.StdEncoding.EncodeToString(aPriv), base64.StdEncoding.EncodeToString(bPub), bPort)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	tun, err := startWireGuard(ctx, conf)
	if err != nil {
		t.Fatal(err)
	}
	defer tun.Close()
	c, err := tun.Dial(ctx, "tcp", "10.9.0.2:2222")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(10 * time.Second))
	c.Write([]byte("ping over wireguard"))
	buf := make([]byte, 64)
	n, err := io.ReadAtLeast(c, buf, len("ping over wireguard"))
	if err != nil || string(buf[:n]) != "ping over wireguard" {
		t.Fatalf("echo failed: %q %v", buf[:n], err)
	}
	if hs, _, _ := tun.stats(); hs.IsZero() {
		t.Fatal("no handshake recorded")
	}
}

func TestParseWGConfigErrors(t *testing.T) {
	for _, bad := range []string{"", "[Interface]\nAddress = 10.0.0.1/32\n", "[Interface]\nPrivateKey = x\nAddress = 10.0.0.1/32\n"} {
		if _, err := parseWGConfig(bad); err == nil {
			t.Errorf("expected error for %q", bad)
		}
	}
}
