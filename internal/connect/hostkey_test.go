package connect

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"errors"
	"testing"

	"golang.org/x/crypto/ssh"
)

func TestHostKeyPinning(t *testing.T) {
	dir := t.TempDir()
	h := NewHub(nil, func(string) string { return dir })
	edPub, _, _ := ed25519.GenerateKey(rand.Reader)
	ed, _ := ssh.NewPublicKey(edPub)
	ec, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	ecKey, _ := ssh.NewPublicKey(&ec.PublicKey)
	other, _, _ := ed25519.GenerateKey(rand.Reader)
	ed2, _ := ssh.NewPublicKey(other)

	cb := h.hostKeyCallback("acme")
	if err := cb("10.0.0.1:22", nil, ed); err != nil {
		t.Fatalf("first use should pin: %v", err)
	}
	if err := cb("10.0.0.1:22", nil, ed); err != nil {
		t.Fatalf("same key rejected: %v", err)
	}
	if err := cb("10.0.0.1:22", nil, ecKey); !errors.Is(err, ErrHostKeyChanged) {
		t.Fatalf("key type switch must be rejected, got %v", err)
	}
	if err := cb("10.0.0.1:22", nil, ed2); !errors.Is(err, ErrHostKeyChanged) {
		t.Fatalf("changed key must be rejected, got %v", err)
	}
	if algos := h.hostKeyAlgorithms("acme", "10.0.0.1:22"); len(algos) != 1 || algos[0] != ssh.KeyAlgoED25519 {
		t.Fatalf("algorithms: %v", algos)
	}
	if err := cb("[10.0.0.1]:2222", nil, ecKey); err != nil {
		t.Fatalf("other port is another host: %v", err)
	}
	h.ForgetHostKey("acme", "10.0.0.1:22")
	if err := cb("10.0.0.1:22", nil, ed2); err != nil {
		t.Fatalf("after forget, TOFU again: %v", err)
	}
}
