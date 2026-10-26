package minisign

import (
	"errors"
	"strings"
	"testing"
)

func TestSignVerify(t *testing.T) {
	k, err := Generate()
	if err != nil {
		t.Fatal(err)
	}
	k2, err := ParsePrivateKey(k.Encode())
	if err != nil || k2.Public().String() != k.Public().String() {
		t.Fatalf("private key round trip: %v", err)
	}
	pub, err := ParsePublicKey("untrusted comment: minisign public key\n" + k.Public().String() + "\n")
	if err != nil {
		t.Fatal(err)
	}
	msg := []byte("abc  opsdeck-cli-1.0.0-linux-amd64\n")
	sig := Sign(k, msg, "opsdeck 1.0.0")
	if tc, err := Verify(pub, msg, sig); err != nil || tc != "opsdeck 1.0.0" {
		t.Fatalf("verify: %q %v", tc, err)
	}
	if _, err := Verify(pub, append(msg, 'x'), sig); !errors.Is(err, ErrSignature) {
		t.Fatalf("a changed file verified: %v", err)
	}
	forged := strings.Replace(string(sig), "opsdeck 1.0.0", "opsdeck 9.9.9", 1)
	if _, err := Verify(pub, msg, []byte(forged)); !errors.Is(err, ErrSignature) {
		t.Fatalf("a changed trusted comment verified: %v", err)
	}
	other, _ := Generate()
	if _, err := Verify(other.Public(), msg, sig); err == nil {
		t.Fatal("verified with another key")
	}
	if _, err := Verify(pub, msg, []byte("garbage")); err == nil {
		t.Fatal("garbage verified")
	}
}
