package dnsserver

import (
	"os"
	"testing"
	"time"
)

// TestServe keeps both fakes running for a manual UI check when
// DNSFAKE_SERVE names a file: it writes their URLs and the token they accept
// there (one per line) and stops when the file is removed (or after 30
// minutes). Skipped otherwise.
func TestServe(t *testing.T) {
	path := os.Getenv("DNSFAKE_SERVE")
	if path == "" {
		t.Skip("set DNSFAKE_SERVE=<file> to run the fakes")
	}
	cf := Cloudflare(t)
	ar := ArvanCloud(t)
	os.WriteFile(path, []byte(cf.URL+"\n"+ar.URL+"\n"+Token+"\n"), 0o644)
	for i := 0; i < 1800; i++ {
		if _, err := os.Stat(path); err != nil {
			return
		}
		time.Sleep(time.Second)
	}
}
