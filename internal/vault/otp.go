package vault

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base32"
	"encoding/binary"
	"errors"
	"fmt"
	"hash"
	"math/big"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// TOTP computes the current code for a base32 secret or an otpauth:// URI.
func TOTP(secretOrURI string, now time.Time) (code string, period int, remaining int, err error) {
	secret, digits, period, algo := strings.TrimSpace(secretOrURI), 6, 30, "SHA1"
	if strings.HasPrefix(secret, "otpauth://") {
		u, err := url.Parse(secret)
		if err != nil {
			return "", 0, 0, err
		}
		q := u.Query()
		secret = q.Get("secret")
		if d, err := strconv.Atoi(q.Get("digits")); err == nil && d >= 6 && d <= 8 {
			digits = d
		}
		if p, err := strconv.Atoi(q.Get("period")); err == nil && p > 0 {
			period = p
		}
		if a := q.Get("algorithm"); a != "" {
			algo = strings.ToUpper(a)
		}
	}
	secret = strings.ToUpper(strings.ReplaceAll(secret, " ", ""))
	key, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(strings.TrimRight(secret, "="))
	if err != nil || len(key) == 0 {
		return "", 0, 0, errors.New("invalid TOTP secret")
	}
	var h func() hash.Hash
	switch algo {
	case "SHA1":
		h = sha1.New
	case "SHA256":
		h = sha256.New
	case "SHA512":
		h = sha512.New
	default:
		return "", 0, 0, fmt.Errorf("unsupported TOTP algorithm %s", algo)
	}
	counter := uint64(now.Unix()) / uint64(period)
	var msg [8]byte
	binary.BigEndian.PutUint64(msg[:], counter)
	mac := hmac.New(h, key)
	mac.Write(msg[:])
	sum := mac.Sum(nil)
	off := sum[len(sum)-1] & 0x0f
	bin := binary.BigEndian.Uint32(sum[off:off+4]) & 0x7fffffff
	mod := uint32(1)
	for i := 0; i < digits; i++ {
		mod *= 10
	}
	code = fmt.Sprintf("%0*d", digits, bin%mod)
	remaining = period - int(now.Unix()%int64(period))
	return code, period, remaining, nil
}

// GeneratePassword returns a random password from a readable alphabet.
func GeneratePassword(length int, symbols bool) (string, error) {
	if length < 8 {
		length = 8
	}
	if length > 256 {
		length = 256
	}
	const letters = "abcdefghijkmnopqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	alphabet := letters
	if symbols {
		alphabet += "!@#$%^&*()-_=+[]{};:,.?"
	}
	var b strings.Builder
	max := big.NewInt(int64(len(alphabet)))
	for i := 0; i < length; i++ {
		n, err := rand.Int(rand.Reader, max)
		if err != nil {
			return "", err
		}
		b.WriteByte(alphabet[n.Int64()])
	}
	return b.String(), nil
}
