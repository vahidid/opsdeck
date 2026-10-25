package dnsx

import (
	"context"
	"strings"
	"testing"

	"opsdeck/internal/config"
	"opsdeck/internal/testutil/dnsserver"
)

func TestNames(t *testing.T) {
	for _, c := range []struct{ in, zone, rel, fqdn string }{
		{"www.example.com", "example.com", "www", "www.example.com"},
		{"example.com", "example.com", "@", "example.com"},
		{"", "example.com", "@", "example.com"},
		{"@", "example.com", "@", "example.com"},
		{"WWW", "Example.com", "www", "www.example.com"},
		{"_dmarc.example.com.", "example.com.", "_dmarc", "_dmarc.example.com"},
		{"*.example.com", "example.com", "*", "*.example.com"},
		{"other.org", "example.com", "other.org", "other.org.example.com"},
	} {
		if got := Relative(c.in, c.zone); got != c.rel {
			t.Errorf("Relative(%q, %q) = %q, want %q", c.in, c.zone, got, c.rel)
		}
		if got := FQDN(c.in, c.zone); got != c.fqdn {
			t.Errorf("FQDN(%q, %q) = %q, want %q", c.in, c.zone, got, c.fqdn)
		}
	}
	if w, p, tgt, err := SRV("5 5060 sip.example.com."); err != nil || w != 5 || p != 5060 || tgt != "sip.example.com" {
		t.Fatalf("SRV: %d %d %q %v", w, p, tgt, err)
	}
	if _, _, _, err := SRV("5 sip"); err == nil {
		t.Fatal("SRV accepted two fields")
	}
	if f, tag, v, err := CAA(`0 issue "letsencrypt.org"`); err != nil || f != 0 || tag != "issue" || v != "letsencrypt.org" {
		t.Fatalf("CAA: %d %s %q %v", f, tag, v, err)
	}
	if _, tag, v, err := CAA(`iodef mailto:ops@example.com`); err != nil || tag != "iodef" || v != "mailto:ops@example.com" {
		t.Fatalf("CAA without flags: %s %q %v", tag, v, err)
	}
	in := RecordInput{Type: "mx", Name: "Example.com", Content: " mail.example.com "}
	if err := Validate(&in, "example.com"); err != nil || in.Type != "MX" || in.Name != "@" || in.Content != "mail.example.com" {
		t.Fatalf("Validate: %+v %v", in, err)
	}
	yes := true
	if err := Validate(&RecordInput{Type: "TXT", Name: "x", Content: "y", Proxied: &yes}, "example.com"); err == nil {
		t.Fatal("a proxied TXT record was accepted")
	}
	line := Format(Record{FQDN: "example.com", Type: "MX", Content: "mail.example.com", Priority: 10, TTL: 3600})
	if !strings.Contains(line, "3600") || !strings.Contains(line, "MX") || !strings.Contains(line, "10 mail.example.com") {
		t.Fatalf("Format: %q", line)
	}
}

// TestCloudflare: zones and records in the neutral shape, and the bodies
// sent for a plain record, an SRV record and an update.
func TestCloudflare(t *testing.T) {
	f := dnsserver.Cloudflare(t)
	ctx := context.Background()
	p, err := Open(config.DNSConnection{ID: "cf", Provider: "cloudflare", APIURL: f.URL}, dnsserver.Token)
	if err != nil {
		t.Fatal(err)
	}
	acct, n, err := p.Verify(ctx)
	if err != nil || acct != "Acme Account" || n != 1 {
		t.Fatalf("verify: %q %d %v", acct, n, err)
	}
	if _, _, err := mustOpen(t, "cloudflare", f.URL, "wrong").Verify(ctx); err == nil || !strings.Contains(err.Error(), "not accepted") {
		t.Fatalf("wrong token: %v", err)
	}
	z, err := p.Zone(ctx, "example.com")
	if err != nil || z.ID != "z1" || z.Status != "active" || len(z.NameServers) != 2 || z.Plan != "Free Website" || !strings.Contains(z.URL, "example.com") {
		t.Fatalf("zone: %+v %v", z, err)
	}
	if _, err := p.Zone(ctx, "nope.com"); err == nil || !strings.Contains(err.Error(), "not in this Cloudflare account") {
		t.Fatalf("missing zone: %v", err)
	}
	recs, err := p.Records(ctx, z)
	if err != nil || len(recs) != 2 {
		t.Fatalf("records: %+v %v", recs, err)
	}
	// zone itself first, relative names, proxied flag, priority
	if recs[0].Name != "@" || recs[0].Type != "MX" || recs[0].Priority != 10 || recs[0].Content != "mail.example.com" || recs[0].TTL != 3600 {
		t.Fatalf("mx: %+v", recs[0])
	}
	if recs[1].Name != "www" || recs[1].FQDN != "www.example.com" || !recs[1].Proxied || !recs[1].Proxiable || recs[1].Comment != "web" || recs[1].TTL != 1 {
		t.Fatalf("a: %+v", recs[1])
	}

	// create an SRV record: name becomes the FQDN, the value goes into data
	srv, err := p.Create(ctx, z, RecordInput{Type: "srv", Name: "_sip._tcp", Content: "5 5060 sip.example.com", Priority: 10, TTL: 300})
	if err != nil || srv.ID == "" || srv.Type != "SRV" || srv.Name != "_sip._tcp" || srv.Content != "5 5060 sip.example.com" || srv.Priority != 10 {
		t.Fatalf("srv: %+v %v", srv, err)
	}
	yes := true
	a, err := p.Create(ctx, z, RecordInput{Type: "A", Name: "api", Content: "203.0.113.20", Proxied: &yes})
	if err != nil || a.FQDN != "api.example.com" || !a.Proxied || a.TTL != 1 {
		t.Fatalf("a: %+v %v", a, err)
	}
	if _, err := p.Create(ctx, z, RecordInput{Type: "A", Name: "api", Content: "203.0.113.20", Proxied: &yes}); err == nil || !strings.Contains(err.Error(), "identical record") {
		t.Fatalf("duplicate: %v", err)
	}
	if _, err := p.Create(ctx, z, RecordInput{Type: "A", Name: "x", Content: "bad"}); err == nil || !strings.Contains(err.Error(), "valid IPv4") {
		t.Fatalf("bad content: %v", err)
	}
	if _, err := p.Create(ctx, z, RecordInput{Type: "A", Name: "x", Content: "1.1.1.1, 2.2.2.2"}); err == nil || !strings.Contains(err.Error(), "one address") {
		t.Fatalf("two addresses: %v", err)
	}
	no := false
	upd, err := p.Update(ctx, z, a, RecordInput{Type: "A", Name: "api.example.com", Content: "203.0.113.21", TTL: 120, Proxied: &no, Comment: "moved"})
	if err != nil || upd.ID != a.ID || upd.Content != "203.0.113.21" || upd.Proxied || upd.TTL != 120 || upd.Comment != "moved" {
		t.Fatalf("update: %+v %v", upd, err)
	}
	if err := p.Delete(ctx, z, a.ID); err != nil {
		t.Fatal(err)
	}
	if err := p.Delete(ctx, z, a.ID); err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("delete twice: %v", err)
	}
	recs, _ = p.Records(ctx, z)
	if len(recs) != 3 {
		t.Fatalf("after: %d records", len(recs))
	}
	for _, s := range f.Seen {
		if strings.Contains(s, dnsserver.Token) {
			t.Fatal("token in a URL")
		}
	}
}

// TestArvanCloud: typed values become text (several addresses in one A
// record), lowercase types are normalized, and the create body carries the
// provider's defaults.
func TestArvanCloud(t *testing.T) {
	f := dnsserver.ArvanCloud(t)
	ctx := context.Background()
	p := mustOpen(t, "arvancloud", f.URL, dnsserver.Token)
	if _, n, err := p.Verify(ctx); err != nil || n != 1 {
		t.Fatalf("verify: %d %v", n, err)
	}
	if _, _, err := mustOpen(t, "arvancloud", f.URL, "wrong").Verify(ctx); err == nil || !strings.Contains(err.Error(), "Unauthenticated") {
		t.Fatalf("wrong key: %v", err)
	}
	zones, err := p.Zones(ctx, "exam")
	if err != nil || len(zones) != 1 || zones[0].Name != "example.ir" || zones[0].NameServers[0] != "ns1.arvancdn.ir" {
		t.Fatalf("zones: %+v %v", zones, err)
	}
	z, err := p.Zone(ctx, "example.ir")
	if err != nil || z.Name != "example.ir" || z.Status != "active" {
		t.Fatalf("zone: %+v %v", z, err)
	}
	recs, err := p.Records(ctx, z)
	if err != nil || len(recs) != 2 {
		t.Fatalf("records: %+v %v", recs, err)
	}
	if recs[0].Type != "TXT" || recs[0].Name != "@" || recs[0].Content != "v=spf1 -all" || recs[0].FQDN != "example.ir" {
		t.Fatalf("txt: %+v", recs[0])
	}
	if recs[1].Type != "A" || recs[1].Content != "198.51.100.1, 198.51.100.2" || !recs[1].Proxied || recs[1].TTL != 120 {
		t.Fatalf("a: %+v", recs[1])
	}
	yes := true
	mx, err := p.Create(ctx, z, RecordInput{Type: "MX", Name: "@", Content: "mail.example.ir.", Priority: 20})
	if err != nil || mx.Type != "MX" || mx.Content != "mail.example.ir" || mx.Priority != 20 || mx.TTL != 120 {
		t.Fatalf("mx: %+v %v", mx, err)
	}
	cname, err := p.Create(ctx, z, RecordInput{Type: "cname", Name: "shop.example.ir", Content: "shop.cdn.example", TTL: 300, Proxied: &yes})
	if err != nil || cname.Name != "shop" || cname.Content != "shop.cdn.example" || !cname.Proxied || cname.TTL != 300 {
		t.Fatalf("cname: %+v %v", cname, err)
	}
	a, err := p.Create(ctx, z, RecordInput{Type: "A", Name: "lb", Content: "10.0.0.1 10.0.0.2"})
	if err != nil || a.Content != "10.0.0.1, 10.0.0.2" {
		t.Fatalf("multi a: %+v %v", a, err)
	}
	sent := f.Zones["example.ir"][len(f.Zones["example.ir"])-1]
	if sent["type"] != "a" || sent["upstream_https"] != "default" || sent["ip_filter_mode"] == nil {
		t.Fatalf("body: %v", sent)
	}
	upd, err := p.Update(ctx, z, a, RecordInput{Type: "A", Name: "lb", Content: "10.0.0.3", TTL: 600})
	if err != nil || upd.ID != a.ID || upd.Content != "10.0.0.3" || upd.TTL != 600 {
		t.Fatalf("update: %+v %v", upd, err)
	}
	if err := p.Delete(ctx, z, a.ID); err != nil {
		t.Fatal(err)
	}
	if err := p.Delete(ctx, z, "zz"); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("delete missing: %v", err)
	}
	if _, err := p.Create(ctx, z, RecordInput{Type: "TLSA", Name: "x", Content: "y"}); err == nil || !strings.Contains(err.Error(), "not supported") {
		t.Fatalf("tlsa: %v", err)
	}
}

func mustOpen(t *testing.T, provider, apiURL, token string) Provider {
	t.Helper()
	p, err := Open(config.DNSConnection{ID: provider, Provider: provider, APIURL: apiURL}, token)
	if err != nil {
		t.Fatal(err)
	}
	return p
}
