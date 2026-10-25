package acmeserver

import (
	"os"
	"testing"
	"time"

	"opsdeck/internal/testutil/dnsserver"
)

// TestServe keeps a fake Cloudflare and a fake CA that validates against it
// running for a manual UI check when ACMEFAKE_SERVE names a file: it writes
// the DNS API URL, the ACME directory URL and the DNS token there (one per
// line) and stops when the file is removed (or after 30 minutes). Skipped
// otherwise.
func TestServe(t *testing.T) {
	path := os.Getenv("ACMEFAKE_SERVE")
	if path == "" {
		t.Skip("set ACMEFAKE_SERVE=<file> to run the fakes")
	}
	dns := dnsserver.Cloudflare(t)
	ca := New(t, dns)
	os.WriteFile(path, []byte(dns.URL+"\n"+ca.URL+"\n"+dnsserver.Token+"\n"), 0o644)
	for i := 0; i < 1800; i++ {
		if _, err := os.Stat(path); err != nil {
			return
		}
		time.Sleep(time.Second)
	}
}
