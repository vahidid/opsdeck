package ansible

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

// Ansible Vault, format 1.1 and 1.2 (AES256), implemented natively so
// files can be viewed, edited and encrypted without ansible-vault and the
// password never has to be written anywhere:
//
//	$ANSIBLE_VAULT;1.1;AES256            (1.2 adds ;<vault id>)
//	hex( hex(salt) \n hex(hmac) \n hex(ciphertext) ), wrapped at 80 columns
//
// key material = PBKDF2-SHA256(password, salt, 10000 iterations, 80 bytes):
// 32 bytes AES-256-CTR key, 32 bytes HMAC-SHA256 key, 16 bytes counter.
// The plaintext is PKCS#7 padded; the HMAC covers the ciphertext.

const (
	vaultMagic  = "$ANSIBLE_VAULT"
	kdfIter     = 10000
	saltLen     = 32
	wrapColumns = 80
)

// ErrWrongPassword is returned when the password does not open a vault.
var ErrWrongPassword = errors.New("wrong vault password")

// Header describes an encrypted file or value.
type Header struct {
	Version string `json:"version"` // 1.1 or 1.2
	Cipher  string `json:"cipher"`
	ID      string `json:"id"` // vault id ("" = default)
}

// IsEncrypted reports whether data is an Ansible Vault (a whole file or the
// text of an inline value).
func IsEncrypted(data []byte) bool {
	return bytes.HasPrefix(bytes.TrimLeft(data, " \t\r\n"), []byte(vaultMagic+";"))
}

// ParseHeader reads the header line of vault text.
func ParseHeader(text string) (Header, error) {
	text = strings.TrimLeft(text, " \t\r\n")
	line, _, _ := strings.Cut(text, "\n")
	parts := strings.Split(strings.TrimSpace(line), ";")
	if len(parts) < 3 || parts[0] != vaultMagic {
		return Header{}, errors.New("not an Ansible Vault")
	}
	h := Header{Version: parts[1], Cipher: parts[2]}
	if len(parts) > 3 {
		h.ID = strings.TrimSpace(parts[3])
	}
	if h.Version != "1.1" && h.Version != "1.2" {
		return h, fmt.Errorf("unsupported vault format %s", h.Version)
	}
	if h.Cipher != "AES256" {
		return h, fmt.Errorf("unsupported vault cipher %s", h.Cipher)
	}
	return h, nil
}

func deriveKeys(password, salt []byte) (aesKey, hmacKey, iv []byte, err error) {
	km, err := pbkdf2.Key(sha256.New, string(password), salt, kdfIter, 2*32+aes.BlockSize)
	if err != nil {
		return nil, nil, nil, err
	}
	return km[:32], km[32:64], km[64:], nil
}

// Decrypt opens vault text (with or without leading indentation, as found
// in an inline !vault value) and returns the plaintext.
func Decrypt(text, password string) ([]byte, error) {
	if _, err := ParseHeader(text); err != nil {
		return nil, err
	}
	var body strings.Builder
	for i, line := range strings.Split(strings.TrimSpace(text), "\n") {
		if i == 0 {
			continue
		}
		body.WriteString(strings.TrimSpace(line))
	}
	inner, err := hex.DecodeString(body.String())
	if err != nil {
		return nil, errors.New("damaged vault: " + err.Error())
	}
	parts := strings.Split(string(inner), "\n")
	if len(parts) != 3 {
		return nil, errors.New("damaged vault: unexpected layout")
	}
	salt, err1 := hex.DecodeString(parts[0])
	mac, err2 := hex.DecodeString(parts[1])
	ct, err3 := hex.DecodeString(parts[2])
	if err := errors.Join(err1, err2, err3); err != nil {
		return nil, errors.New("damaged vault: " + err.Error())
	}
	aesKey, hmacKey, iv, err := deriveKeys([]byte(password), salt)
	if err != nil {
		return nil, err
	}
	h := hmac.New(sha256.New, hmacKey)
	h.Write(ct)
	if !hmac.Equal(h.Sum(nil), mac) {
		return nil, ErrWrongPassword
	}
	block, err := aes.NewCipher(aesKey)
	if err != nil {
		return nil, err
	}
	plain := make([]byte, len(ct))
	cipher.NewCTR(block, iv).XORKeyStream(plain, ct)
	return unpad(plain)
}

// Encrypt returns vault text for plaintext. A vault id makes it format 1.2.
// The text ends with a newline.
func Encrypt(plain []byte, password, id string) (string, error) {
	if password == "" {
		return "", errors.New("the vault password is empty")
	}
	if strings.ContainsAny(id, ";\n\r") {
		return "", errors.New("a vault id cannot contain ';' or line breaks")
	}
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	aesKey, hmacKey, iv, err := deriveKeys([]byte(password), salt)
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(aesKey)
	if err != nil {
		return "", err
	}
	padded := pad(plain)
	ct := make([]byte, len(padded))
	cipher.NewCTR(block, iv).XORKeyStream(ct, padded)
	h := hmac.New(sha256.New, hmacKey)
	h.Write(ct)
	inner := hex.EncodeToString(salt) + "\n" + hex.EncodeToString(h.Sum(nil)) + "\n" + hex.EncodeToString(ct)
	body := hex.EncodeToString([]byte(inner))

	var b strings.Builder
	if id != "" {
		b.WriteString(vaultMagic + ";1.2;AES256;" + id + "\n")
	} else {
		b.WriteString(vaultMagic + ";1.1;AES256\n")
	}
	for len(body) > wrapColumns {
		b.WriteString(body[:wrapColumns] + "\n")
		body = body[wrapColumns:]
	}
	b.WriteString(body + "\n")
	return b.String(), nil
}

func pad(b []byte) []byte {
	n := aes.BlockSize - len(b)%aes.BlockSize
	return append(append([]byte{}, b...), bytes.Repeat([]byte{byte(n)}, n)...)
}

func unpad(b []byte) ([]byte, error) {
	if len(b) == 0 || len(b)%aes.BlockSize != 0 {
		return nil, errors.New("damaged vault: bad length")
	}
	n := int(b[len(b)-1])
	if n == 0 || n > aes.BlockSize || n > len(b) {
		return nil, errors.New("damaged vault: bad padding")
	}
	for _, c := range b[len(b)-n:] {
		if int(c) != n {
			return nil, errors.New("damaged vault: bad padding")
		}
	}
	return b[:len(b)-n], nil
}
