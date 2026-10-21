package vault

import (
	"crypto/rand"
	"errors"
	"fmt"

	"golang.org/x/crypto/argon2"
	"golang.org/x/crypto/chacha20poly1305"
)

// KDFParams are stored in the vault header so they can be raised later
// without breaking existing vaults.
type KDFParams struct {
	Alg     string `json:"alg"` // "argon2id"
	Salt    []byte `json:"salt"`
	Time    uint32 `json:"time"`
	MemKiB  uint32 `json:"memKiB"`
	Threads uint8  `json:"threads"`
}

func defaultKDF() (KDFParams, error) {
	salt, err := randomBytes(16)
	if err != nil {
		return KDFParams{}, err
	}
	return KDFParams{Alg: "argon2id", Salt: salt, Time: 3, MemKiB: 64 * 1024, Threads: 4}, nil
}

func (p KDFParams) derive(password string) ([]byte, error) {
	if p.Alg != "argon2id" {
		return nil, fmt.Errorf("unsupported kdf %q", p.Alg)
	}
	return argon2.IDKey([]byte(password), p.Salt, p.Time, p.MemKiB, p.Threads, 32), nil
}

func randomBytes(n int) ([]byte, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return nil, err
	}
	return b, nil
}

// seal encrypts with XChaCha20-Poly1305; output is nonce || ciphertext.
func seal(key, plaintext, aad []byte) ([]byte, error) {
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return nil, err
	}
	nonce, err := randomBytes(aead.NonceSize())
	if err != nil {
		return nil, err
	}
	return aead.Seal(nonce, nonce, plaintext, aad), nil
}

var errDecrypt = errors.New("decryption failed")

func open(key, sealed, aad []byte) ([]byte, error) {
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return nil, err
	}
	if len(sealed) < aead.NonceSize()+aead.Overhead() {
		return nil, errDecrypt
	}
	nonce, ct := sealed[:aead.NonceSize()], sealed[aead.NonceSize():]
	pt, err := aead.Open(nil, nonce, ct, aad)
	if err != nil {
		return nil, errDecrypt
	}
	return pt, nil
}

// wipe zeroes key material. Best effort: Go's GC may have copied it, but this
// keeps live keys from lingering in the buffers we own.
func wipe(b []byte) {
	for i := range b {
		b[i] = 0
	}
}

func aadVK(vaultID string) []byte { return []byte("opsdeck/vk|" + vaultID) }
func aadScope(vaultID, scope string) []byte {
	return []byte("opsdeck/scope|" + vaultID + "|" + scope)
}
func aadItem(vaultID, scope, id string, rev int) []byte {
	return []byte(fmt.Sprintf("opsdeck/item|%s|%s|%s|%d", vaultID, scope, id, rev))
}
