// Package minisign signs and verifies files in the minisign format
// (https://jedisct1.github.io/minisign/): Ed25519 over the file's BLAKE2b-512
// hash, plus a signature over the trusted comment. opsdeck signs each
// release's SHA256SUMS.txt this way; the updater checks it, and anyone can
// with `minisign -Vm SHA256SUMS.txt -P <public key>`.
package minisign

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/blake2b"
)

const (
	algPrehashed = "ED" // signature over BLAKE2b-512 of the file (minisign's default)
	algLegacy    = "Ed" // signature over the file itself
)

// PublicKey is a minisign public key.
type PublicKey struct {
	ID  [8]byte
	Key ed25519.PublicKey
}

// PrivateKey is a signing key: an Ed25519 key and its minisign key id. It is
// kept as one base64 string (see Encode), not in minisign's encrypted
// secret-key file format.
type PrivateKey struct {
	ID  [8]byte
	Key ed25519.PrivateKey
}

// Generate makes a new key pair.
func Generate() (PrivateKey, error) {
	_, sk, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return PrivateKey{}, err
	}
	var k PrivateKey
	rand.Read(k.ID[:])
	k.Key = sk
	return k, nil
}

// Public returns the public half.
func (k PrivateKey) Public() PublicKey {
	return PublicKey{ID: k.ID, Key: k.Key.Public().(ed25519.PublicKey)}
}

// Encode is "Ed" + id + the 32-byte seed, base64.
func (k PrivateKey) Encode() string {
	return base64.StdEncoding.EncodeToString(append(append([]byte(algLegacy), k.ID[:]...), k.Key.Seed()...))
}

// ParsePrivateKey reads an encoded private key.
func ParsePrivateKey(s string) (PrivateKey, error) {
	b, err := base64.StdEncoding.DecodeString(strings.TrimSpace(s))
	if err != nil || len(b) != 2+8+ed25519.SeedSize || string(b[:2]) != algLegacy {
		return PrivateKey{}, errors.New("not an opsdeck signing key")
	}
	var k PrivateKey
	copy(k.ID[:], b[2:10])
	k.Key = ed25519.NewKeyFromSeed(b[10:])
	return k, nil
}

// String is the public key as minisign prints it (the base64 line).
func (p PublicKey) String() string {
	return base64.StdEncoding.EncodeToString(append(append([]byte(algLegacy), p.ID[:]...), p.Key...))
}

// IDString is the key id as minisign shows it (hex, most significant first).
func (p PublicKey) IDString() string {
	return fmt.Sprintf("%016X", binary.LittleEndian.Uint64(p.ID[:]))
}

// ParsePublicKey reads a public key: the base64 line, or a .pub file.
func ParsePublicKey(s string) (PublicKey, error) {
	s = strings.TrimSpace(s)
	if lines := strings.Split(s, "\n"); len(lines) > 1 {
		s = strings.TrimSpace(lines[len(lines)-1])
	}
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil || len(b) != 2+8+ed25519.PublicKeySize || string(b[:2]) != algLegacy {
		return PublicKey{}, errors.New("not a minisign public key")
	}
	var p PublicKey
	copy(p.ID[:], b[2:10])
	p.Key = ed25519.PublicKey(append([]byte(nil), b[10:]...))
	return p, nil
}

// Sign returns the .minisig file for msg.
func Sign(k PrivateKey, msg []byte, trustedComment string) []byte {
	h := blake2b.Sum512(msg)
	sig := ed25519.Sign(k.Key, h[:])
	global := ed25519.Sign(k.Key, append(append([]byte(nil), sig...), trustedComment...))
	var b bytes.Buffer
	fmt.Fprintf(&b, "untrusted comment: signature from opsdeck release key %s\n", k.Public().IDString())
	b.WriteString(base64.StdEncoding.EncodeToString(append(append([]byte(algPrehashed), k.ID[:]...), sig...)) + "\n")
	b.WriteString("trusted comment: " + trustedComment + "\n")
	b.WriteString(base64.StdEncoding.EncodeToString(global) + "\n")
	return b.Bytes()
}

// ErrSignature means a signature does not match.
var ErrSignature = errors.New("signature verification failed")

// Verify checks a .minisig file against msg and returns its trusted comment.
func Verify(p PublicKey, msg, sigFile []byte) (string, error) {
	lines := strings.Split(strings.ReplaceAll(string(sigFile), "\r\n", "\n"), "\n")
	if len(lines) < 4 || !strings.HasPrefix(lines[0], "untrusted comment:") || !strings.HasPrefix(lines[2], "trusted comment: ") {
		return "", errors.New("not a minisign signature")
	}
	sb, err := base64.StdEncoding.DecodeString(strings.TrimSpace(lines[1]))
	if err != nil || len(sb) != 2+8+ed25519.SignatureSize {
		return "", errors.New("not a minisign signature")
	}
	alg, id, sig := string(sb[:2]), sb[2:10], sb[10:]
	if !bytes.Equal(id, p.ID[:]) {
		return "", fmt.Errorf("signed with key %X, not %s", binary.LittleEndian.Uint64(id), p.IDString())
	}
	signed := msg
	switch alg {
	case algPrehashed:
		h := blake2b.Sum512(msg)
		signed = h[:]
	case algLegacy:
	default:
		return "", fmt.Errorf("unknown signature algorithm %q", alg)
	}
	if !ed25519.Verify(p.Key, signed, sig) {
		return "", ErrSignature
	}
	trusted := strings.TrimPrefix(lines[2], "trusted comment: ")
	global, err := base64.StdEncoding.DecodeString(strings.TrimSpace(lines[3]))
	if err != nil || !ed25519.Verify(p.Key, append(append([]byte(nil), sig...), trusted...), global) {
		return "", ErrSignature
	}
	return trusted, nil
}

// Fingerprint is a short hex digest of a public key, for logs.
func (p PublicKey) Fingerprint() string { return hex.EncodeToString(p.ID[:]) }
