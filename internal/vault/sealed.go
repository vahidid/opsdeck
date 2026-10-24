package vault

import "errors"

// ErrBadFilePassword is returned when a Sealed box does not open.
var ErrBadFilePassword = errors.New("wrong password, or the file is damaged")

// Sealed is data encrypted under a key derived from a password rather than
// the vault key, for files that leave the vault (workspace exports): anyone
// with the password can open it on any machine.
type Sealed struct {
	KDF  KDFParams `json:"kdf"`
	Data []byte    `json:"data"` // nonce || ciphertext
}

// SealWithPassword encrypts plaintext under password; aad binds it to its use.
func SealWithPassword(password string, plaintext, aad []byte) (Sealed, error) {
	if len(password) < 8 {
		return Sealed{}, errors.New("password must be at least 8 characters")
	}
	kdf, err := defaultKDF()
	if err != nil {
		return Sealed{}, err
	}
	key, err := kdf.derive(password)
	if err != nil {
		return Sealed{}, err
	}
	defer wipe(key)
	data, err := seal(key, plaintext, aad)
	if err != nil {
		return Sealed{}, err
	}
	return Sealed{KDF: kdf, Data: data}, nil
}

// Open decrypts the box.
func (s Sealed) Open(password string, aad []byte) ([]byte, error) {
	// The parameters come from the file: bound them so that a crafted file
	// cannot make the key derivation use gigabytes of memory or run forever.
	p := s.KDF
	if p.Time < 1 || p.Time > 16 || p.Threads < 1 || p.Threads > 16 || p.MemKiB > 256*1024 {
		return nil, errors.New("unsupported key derivation parameters")
	}
	key, err := p.derive(password)
	if err != nil {
		return nil, err
	}
	defer wipe(key)
	pt, err := open(key, s.Data, aad)
	if err != nil {
		return nil, ErrBadFilePassword
	}
	return pt, nil
}
