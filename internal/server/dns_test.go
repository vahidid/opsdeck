package server

import (
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"opsdeck/internal/paths"
	"opsdeck/internal/testutil/dnsserver"
)

// TestDNSConnectors: connect a Cloudflare and an ArvanCloud account (a wrong
// token is refused), list their zones, add a domain to a workspace and read,
// create, change and delete its records; disconnecting removes the token.
func TestDNSConnectors(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("OPSDECK_HOME", t.TempDir())
	srv, err := New(paths.Default(), Options{Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	jar, _ := cookiejar.New(nil)
	c := &client{t: t, base: ts.URL, http: &http.Client{Jar: jar, Timeout: 60 * time.Second}}
	c.must("POST", "/api/auth/login", map[string]string{"token": srv.token}, nil)
	c.must("POST", "/api/vault/init", map[string]string{"password": "correct horse battery"}, nil)
	c.must("POST", "/api/workspaces", map[string]string{"name": "acme"}, nil)
	c.must("PUT", "/api/workspaces/acme", map[string]any{"metadata": map[string]any{"name": "acme"}, "environments": []map[string]any{{"name": "prod", "protected": true}}}, nil)

	cf := dnsserver.Cloudflare(t)
	ar := dnsserver.ArvanCloud(t)

	var providers []map[string]any
	c.must("GET", "/api/dns/providers", nil, &providers)
	if len(providers) != 2 || providers[0]["id"] != "cloudflare" || providers[1]["id"] != "arvancloud" {
		t.Fatalf("providers: %v", providers)
	}

	// A wrong token is refused; the right one is checked against the account.
	if msg := failure(c, "POST", "/api/dns/connections", map[string]any{"provider": "cloudflare", "apiUrl": cf.URL, "token": "nope"}); !strings.Contains(msg, "not accepted") {
		t.Fatalf("bad token: %v", msg)
	}
	if msg := failure(c, "POST", "/api/dns/connections", map[string]any{"provider": "route53", "token": "x"}); !strings.Contains(msg, "cloudflare or arvancloud") {
		t.Fatalf("bad provider: %v", msg)
	}
	var conn map[string]any
	c.must("POST", "/api/dns/connections", map[string]any{"provider": "cloudflare", "apiUrl": cf.URL, "token": dnsserver.Token}, &conn)
	if conn["id"] != "cloudflare" || conn["account"] != "Acme Account" || conn["zones"] != 1.0 || conn["credential"] != "vault://global/dns-cloudflare" || conn["label"] != "Cloudflare" {
		t.Fatalf("connection: %v", conn)
	}
	var arconn map[string]any
	c.must("POST", "/api/dns/connections", map[string]any{"provider": "arvancloud", "apiUrl": ar.URL, "token": dnsserver.Token}, &arconn)
	if arconn["id"] != "arvancloud" || arconn["zones"] != 1.0 {
		t.Fatalf("arvan connection: %v", arconn)
	}
	// Reconnecting the same account keeps the ID.
	c.must("POST", "/api/dns/connections", map[string]any{"id": "cloudflare", "provider": "cloudflare", "apiUrl": cf.URL, "token": dnsserver.Token}, &conn)
	var list []map[string]any
	c.must("GET", "/api/dns/connections", nil, &list)
	if len(list) != 2 {
		t.Fatalf("connections: %v", list)
	}
	var test map[string]any
	c.must("POST", "/api/dns/connections/cloudflare/test", nil, &test)
	if test["ok"] != true || test["zones"] != 1.0 {
		t.Fatalf("test: %v", test)
	}
	var zones []map[string]any
	c.must("GET", "/api/dns/connections/arvancloud/zones?q=example", nil, &zones)
	if len(zones) != 1 || zones[0]["name"] != "example.ir" {
		t.Fatalf("zones: %v", zones)
	}

	// Domains of a workspace: one names its connector, the other is found
	// by its zone among the connected accounts.
	c.must("POST", "/api/workspaces/acme/resources", map[string]any{"kind": "Domain", "name": "shop", "environment": "prod", "spec": map[string]any{"zone": "example.com", "connection": "cloudflare"}}, nil)
	c.must("POST", "/api/workspaces/acme/resources", map[string]any{"kind": "Domain", "name": "example-ir", "spec": map[string]any{"zone": "example.ir"}}, nil)
	var domains []map[string]any
	c.must("GET", "/api/workspaces/acme/domains", nil, &domains)
	if len(domains) != 2 {
		t.Fatalf("domains: %v", domains)
	}
	byName := map[string]map[string]any{}
	for _, d := range domains {
		byName[d["name"].(string)] = d
	}
	if d := byName["shop"]; d["provider"] != "cloudflare" || d["protected"] != true || d["info"].(map[string]any)["status"] != "active" || d["error"] != nil {
		t.Fatalf("shop: %v", d)
	}
	if d := byName["example-ir"]; d["provider"] != "arvancloud" || d["connection"] != "arvancloud" || d["error"] != nil {
		t.Fatalf("example-ir: %v", d)
	}

	// Records: read, create, change, delete.
	var recs []map[string]any
	c.must("GET", "/api/workspaces/acme/domains/shop/records", nil, &recs)
	if len(recs) != 2 || recs[0]["name"] != "@" || recs[0]["type"] != "MX" || recs[1]["fqdn"] != "www.example.com" || recs[1]["proxied"] != true {
		t.Fatalf("records: %v", recs)
	}
	c.must("GET", "/api/workspaces/acme/domains/shop/records?type=a&q=www", nil, &recs)
	if len(recs) != 1 {
		t.Fatalf("filtered: %v", recs)
	}
	var rec map[string]any
	c.must("POST", "/api/workspaces/acme/domains/shop/records", map[string]any{"type": "CNAME", "name": "blog", "content": "pages.example.net", "ttl": 300, "proxied": true}, &rec)
	if rec["id"] == "" || rec["fqdn"] != "blog.example.com" || rec["proxied"] != true || rec["ttl"] != 300.0 {
		t.Fatalf("created: %v", rec)
	}
	if code := c.do("POST", "/api/workspaces/acme/domains/shop/records", map[string]any{"type": "TXT", "name": "x", "content": "y", "proxied": true}, nil); code != 400 {
		t.Fatalf("proxied TXT: %d", code)
	}
	id := rec["id"].(string)
	c.must("PUT", "/api/workspaces/acme/domains/shop/records/"+id, map[string]any{"type": "CNAME", "name": "blog", "content": "pages2.example.net", "ttl": 600, "proxied": false}, &rec)
	if rec["content"] != "pages2.example.net" || rec["ttl"] != 600.0 || rec["proxied"] != false {
		t.Fatalf("updated: %v", rec)
	}
	c.must("DELETE", "/api/workspaces/acme/domains/shop/records/"+id, nil, nil)
	if code := c.do("DELETE", "/api/workspaces/acme/domains/shop/records/"+id, nil, nil); code < 400 {
		t.Fatalf("second delete: %d", code)
	}
	// ArvanCloud records through the auto-found connector
	c.must("GET", "/api/workspaces/acme/domains/example-ir/records", nil, &recs)
	if len(recs) != 2 || recs[1]["content"] != "198.51.100.1, 198.51.100.2" {
		t.Fatalf("arvan records: %v", recs)
	}
	c.must("POST", "/api/workspaces/acme/domains/example-ir/records", map[string]any{"type": "A", "name": "api", "content": "198.51.100.9"}, &rec)
	if rec["fqdn"] != "api.example.ir" || rec["ttl"] != 120.0 {
		t.Fatalf("arvan created: %v", rec)
	}

	// Every change is in the audit journal.
	var audit []map[string]any
	c.must("GET", "/api/audit?limit=50", nil, &audit)
	actions := map[string]int{}
	for _, e := range audit {
		if e["result"] != "error" {
			actions[e["action"].(string)]++
		}
	}
	if actions["dns.connect"] != 3 || actions["dns.record.create"] != 2 || actions["dns.record.update"] != 1 || actions["dns.record.delete"] != 1 {
		t.Fatalf("audit: %v", actions)
	}

	// Disconnecting removes the token from the vault; the domain then reports it.
	c.must("DELETE", "/api/dns/connections/cloudflare", nil, nil)
	var items []map[string]any
	c.must("GET", "/api/vault/items?scope=global", nil, &items)
	for _, it := range items {
		if it["name"] == "dns-cloudflare" {
			t.Fatal("token left in the vault")
		}
	}
	c.must("GET", "/api/workspaces/acme/domains", nil, &domains)
	for _, d := range domains {
		if d["name"] == "shop" && !strings.Contains(d["error"].(string), "not set up on this computer") {
			t.Fatalf("shop after disconnect: %v", d)
		}
	}
	if code := c.do("GET", "/api/workspaces/acme/domains/shop/records", nil, nil); code != 502 {
		t.Fatalf("records without connector: %d", code)
	}
}
