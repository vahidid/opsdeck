package vault

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"encoding/pem"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/ssh"
)

// GenerateSSHKey creates a new key pair and returns ssh-key item fields.
func GenerateSSHKey(algorithm, comment string) ([]Field, error) {
	var priv any
	switch algorithm {
	case "", "ed25519":
		_, k, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			return nil, err
		}
		priv = k
	case "rsa":
		k, err := rsa.GenerateKey(rand.Reader, 4096)
		if err != nil {
			return nil, err
		}
		priv = k
	case "ecdsa":
		k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			return nil, err
		}
		priv = k
	default:
		return nil, fmt.Errorf("unsupported algorithm %q", algorithm)
	}
	block, err := ssh.MarshalPrivateKey(priv, comment)
	if err != nil {
		return nil, err
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		return nil, err
	}
	return []Field{
		{Name: "private_key", Value: string(pem.EncodeToMemory(block)), Secret: true},
		{Name: "public_key", Value: authorizedKey(signer.PublicKey(), comment)},
	}, nil
}

// ImportSSHKey validates a private key (OpenSSH / PEM) and returns item fields.
func ImportSSHKey(privateKey, passphrase string) ([]Field, error) {
	privateKey = strings.TrimSpace(privateKey) + "\n"
	if strings.Contains(privateKey, "PuTTY-User-Key-File") {
		return nil, errors.New("PuTTY .ppk keys are not supported yet; convert with: puttygen key.ppk -O private-openssh -o key")
	}
	signer, err := parseSigner(privateKey, passphrase)
	if err != nil {
		return nil, err
	}
	fields := []Field{
		{Name: "private_key", Value: privateKey, Secret: true},
		{Name: "public_key", Value: authorizedKey(signer.PublicKey(), "")},
	}
	if passphrase != "" {
		fields = append(fields, Field{Name: "passphrase", Value: passphrase, Secret: true})
	}
	return fields, nil
}

func parseSigner(privateKey, passphrase string) (ssh.Signer, error) {
	var (
		signer ssh.Signer
		err    error
	)
	if passphrase != "" {
		signer, err = ssh.ParsePrivateKeyWithPassphrase([]byte(privateKey), []byte(passphrase))
	} else {
		signer, err = ssh.ParsePrivateKey([]byte(privateKey))
	}
	var missing *ssh.PassphraseMissingError
	if errors.As(err, &missing) {
		return nil, errors.New("the key is encrypted: a passphrase is required")
	}
	if err != nil {
		return nil, fmt.Errorf("invalid private key: %w", err)
	}
	return signer, nil
}

func authorizedKey(pub ssh.PublicKey, comment string) string {
	s := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(pub)))
	if comment != "" {
		s += " " + comment
	}
	return s
}

// Signer builds an ssh.Signer from an ssh-key item (with its certificate when present).
func Signer(it Item) (ssh.Signer, error) {
	if it.Type != "ssh-key" {
		return nil, fmt.Errorf("%s is a %s, not an ssh-key", it.Ref(), it.Type)
	}
	signer, err := parseSigner(it.Get("private_key"), it.Get("passphrase"))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", it.Ref(), err)
	}
	if certText := strings.TrimSpace(it.Get("certificate")); certText != "" {
		pub, _, _, _, err := ssh.ParseAuthorizedKey([]byte(certText))
		if err != nil {
			return nil, fmt.Errorf("%s: invalid certificate: %w", it.Ref(), err)
		}
		cert, ok := pub.(*ssh.Certificate)
		if !ok {
			return nil, fmt.Errorf("%s: certificate field is not an SSH certificate", it.Ref())
		}
		return ssh.NewCertSigner(cert, signer)
	}
	return signer, nil
}

// PublicKeyInfo returns the authorized_keys line and SHA256 fingerprint of an ssh-key item.
func PublicKeyInfo(it Item) (string, string) {
	line := it.Get("public_key")
	if line == "" {
		return "", ""
	}
	pub, _, _, _, err := ssh.ParseAuthorizedKey([]byte(line))
	if err != nil {
		return line, ""
	}
	return line, ssh.FingerprintSHA256(pub)
}
