package update

import (
	"errors"
	"fmt"

	"opsdeck/internal/minisign"
)

// ReleaseKeys are the minisign public keys whose signature on a release's
// SHA256SUMS.txt the updater accepts (docs/releasing.md). A new key is added
// here one release before the old one is dropped, so updates keep working
// across a key change.
var ReleaseKeys = []string{
	releaseKey2026,
}

// SigFile is the signature of SumsFile.
const SigFile = SumsFile + ".minisig"

// TrustsKey reports whether p is one of the release keys.
func TrustsKey(p minisign.PublicKey) bool {
	for _, s := range ReleaseKeys {
		if k, err := minisign.ParsePublicKey(s); err == nil && k.String() == p.String() {
			return true
		}
	}
	return false
}

// VerifySums checks the signature of a SHA256SUMS.txt against the release
// keys and returns its trusted comment.
func VerifySums(sums, sig []byte) (string, error) {
	if len(ReleaseKeys) == 0 {
		return "", errors.New("no release keys are built in")
	}
	var last error
	for _, s := range ReleaseKeys {
		k, err := minisign.ParsePublicKey(s)
		if err != nil {
			return "", fmt.Errorf("release key: %w", err)
		}
		tc, err := minisign.Verify(k, sums, sig)
		if err == nil {
			return tc, nil
		}
		last = err
	}
	return "", fmt.Errorf("%s: %w", SigFile, last)
}
