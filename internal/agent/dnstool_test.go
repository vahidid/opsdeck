package agent

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"opsdeck/internal/config"
	"opsdeck/internal/connect"
	"opsdeck/internal/dnsx"
	"opsdeck/internal/testutil/dnsserver"
	"opsdeck/internal/vault"
	"opsdeck/internal/workspace"
)

// dnsEnv is an Env with a Cloudflare connector (fake API, token in the
// global vault scope) and a workspace with a protected Domain for
// example.com.
func dnsEnv(t *testing.T) (*Env, *dnsserver.Server) {
	t.Helper()
	dir := t.TempDir()
	v, err := vault.Open(filepath.Join(dir, "vault.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { v.Close() })
	if err := v.Init("correct horse battery"); err != nil {
		t.Fatal(err)
	}
	v.EnsureScope("t")
	f := dnsserver.Cloudflare(t)
	conn := config.DNSConnection{ID: "cloudflare", Provider: "cloudflare", APIURL: f.URL, Method: "token", Account: "Acme Account"}
	it, err := v.Create(dnsx.TokenItem("dns-cloudflare", conn, dnsserver.Token))
	if err != nil {
		t.Fatal(err)
	}
	conn.Credential = it.Ref()
	m := dnsx.New(v)
	m.Connections = func() []config.DNSConnection { return []config.DNSConnection{conn} }
	w := &workspace.Workspace{Metadata: workspace.Meta{Name: "t"}, Path: dir, Resources: []*workspace.Resource{
		{Kind: "Domain", Name: "shop", Environment: "prod", Spec: map[string]any{"zone": "example.com", "connection": "cloudflare"}},
		{Kind: "Host", Name: "web-1", Spec: map[string]any{"address": "203.0.113.10"}},
	}}
	w.Environments = []workspace.Environment{{Name: "prod", Protected: true}}
	return &Env{WS: w, Vault: v, Hub: connect.NewHub(v, func(string) string { return dir }), DNS: m}, f
}

func TestDNSTool(t *testing.T) {
	env, f := dnsEnv(t)
	tl := toolDNS()

	// zones: a read over the workspace
	c := prepare(t, tl, env, map[string]any{"action": "zones"})
	if c.Risk != RiskRead {
		t.Fatalf("zones risk: %s", c.Risk)
	}
	out, _ := viewOf(t, c)
	if !strings.Contains(out, "shop: zone example.com env=prod (PROTECTED) at Cloudflare (connector cloudflare): status active") || !strings.Contains(out, "ada.ns.cloudflare.com") ||
		!strings.Contains(out, "registrar points at ns1.registrar.example") {
		t.Fatalf("zones: %s", out)
	}

	// records: the domain's records, relative names, zone-file-like lines
	c = prepare(t, tl, env, map[string]any{"action": "records"})
	if c.Risk != RiskRead || c.Target != "domain/shop" || !c.Protected || c.Environment != "prod" || strings.Join(c.Credentials, ",") != "vault://global/dns-cloudflare" {
		t.Fatalf("records call: %+v", c)
	}
	out, view := viewOf(t, c)
	if !strings.Contains(out, "example.com: 2 records (zone active)") || !strings.Contains(out, "r1 | www.example.com") || !strings.Contains(out, "10 mail.example.com") || !strings.Contains(out, "(proxied)") {
		t.Fatalf("records: %s", out)
	}
	if v := view.(map[string]any); v["kind"] != "dns" || v["zone"] != "example.com" {
		t.Fatalf("view: %v", view)
	}
	out, _ = viewOf(t, prepare(t, tl, env, map[string]any{"action": "records", "type": "mx"}))
	if !strings.Contains(out, "1 records") {
		t.Fatalf("filtered: %s", out)
	}

	// set on an existing record: a change with before/after; values left
	// out are kept
	c = prepare(t, tl, env, map[string]any{"action": "set", "type": "A", "name": "www", "content": "203.0.113.11"})
	if c.Risk != RiskChange || c.Match != "update A www.example.com" || !strings.Contains(c.Preview, "- www.example.com") || !strings.Contains(c.Preview, "203.0.113.10  (proxied)") ||
		!strings.Contains(c.Preview, "+ www.example.com") || !strings.Contains(c.Preview, "203.0.113.11  (proxied)  ; web") {
		t.Fatalf("set call: %s\n%s", c.Match, c.Preview)
	}
	out, _ = viewOf(t, c)
	if !strings.Contains(out, "Changed in example.com (id r1)") || !strings.Contains(out, "203.0.113.11") {
		t.Fatalf("set: %s", out)
	}
	// nothing to change
	if _, err := tl.Prepare(context.Background(), env, raw(map[string]any{"action": "set", "type": "A", "name": "www", "content": "203.0.113.11"})); err == nil || !strings.Contains(err.Error(), "already has these values") {
		t.Fatalf("no-op set: %v", err)
	}
	// set on a new name creates
	c = prepare(t, tl, env, map[string]any{"action": "set", "type": "TXT", "name": "_dmarc", "content": "v=DMARC1; p=none", "ttl": 300})
	if c.Risk != RiskChange || c.Match != "create TXT _dmarc.example.com" || !strings.Contains(c.Preview, "add to example.com") {
		t.Fatalf("create call: %+v", c)
	}
	out, _ = viewOf(t, c)
	if !strings.Contains(out, "Added to example.com") || !strings.Contains(out, "v=DMARC1; p=none") {
		t.Fatalf("create: %s", out)
	}
	// a proxied TXT is refused before anything is sent
	if _, err := tl.Prepare(context.Background(), env, raw(map[string]any{"action": "set", "type": "TXT", "name": "x", "content": "y", "proxied": true})); err == nil || !strings.Contains(err.Error(), "cannot be proxied") {
		t.Fatalf("proxied txt: %v", err)
	}
	// a second A record for the same name next to the first (content differs) creates
	c = prepare(t, tl, env, map[string]any{"action": "set", "type": "A", "name": "www", "content": "203.0.113.12"})
	if !strings.HasPrefix(c.Match, "update") {
		t.Fatalf("second A should update the single existing one: %s", c.Match)
	}

	// delete: destructive, needs an unambiguous record
	c = prepare(t, tl, env, map[string]any{"action": "delete", "type": "MX", "name": "@"})
	if c.Risk != RiskDestructive || c.Match != "delete MX example.com" || !strings.Contains(c.Preview, "delete from example.com") || !strings.Contains(c.Preview, "mail.example.com") {
		t.Fatalf("delete call: %+v", c)
	}
	out, _ = viewOf(t, c)
	if !strings.Contains(out, "Deleted from example.com") {
		t.Fatalf("delete: %s", out)
	}
	if _, err := tl.Prepare(context.Background(), env, raw(map[string]any{"action": "delete", "type": "MX", "name": "@"})); err == nil || !strings.Contains(err.Error(), "exactly one record") {
		t.Fatalf("delete missing: %v", err)
	}
	if _, err := tl.Prepare(context.Background(), env, raw(map[string]any{"action": "delete", "id": "nope"})); err == nil || !strings.Contains(err.Error(), "no record with id") {
		t.Fatalf("delete bad id: %v", err)
	}
	// the token never shows in outputs or previews
	for _, s := range []string{out, c.Preview} {
		if strings.Contains(s, dnsserver.Token) {
			t.Fatal("token leaked")
		}
	}
	// unknown domain, unknown action
	for _, bad := range []map[string]any{{"action": "records", "domain": "nope"}, {"action": "flush"}} {
		if _, err := tl.Prepare(context.Background(), env, raw(bad)); err == nil {
			t.Errorf("accepted %v", bad)
		}
	}
	// the tool is offered only with a Domain
	names := map[string]bool{}
	for _, tl := range availableTools(env.WS, "", false) {
		names[tl.Def.Name] = true
	}
	if !names["dns"] {
		t.Fatal("dns tool not offered")
	}
	env.WS.Resources = env.WS.Resources[1:]
	for _, tl := range availableTools(env.WS, "", false) {
		if tl.Def.Name == "dns" {
			t.Fatal("dns tool offered without a Domain")
		}
	}
	_ = f
}

func raw(in map[string]any) json.RawMessage {
	b, _ := json.Marshal(in)
	return b
}
