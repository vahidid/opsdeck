package acmex

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"opsdeck/internal/config"
	"opsdeck/internal/dnsx"
	"opsdeck/internal/testutil/acmeserver"
	"opsdeck/internal/testutil/dnsserver"
	"opsdeck/internal/vault"
	"opsdeck/internal/workspace"
)

// env: a vault with scope t, a Cloudflare connector pointing at the DNS
// fake (zone example.com), a fake CA that checks the challenge records
// there, and a workspace with a Certificate for example.com and its
// wildcard.
func env(t *testing.T) (*Manager, *workspace.Workspace, *acmeserver.Server, *dnsserver.Server) {
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
	dns := dnsserver.Cloudflare(t)
	conn := config.DNSConnection{ID: "cloudflare", Provider: "cloudflare", APIURL: dns.URL, Method: "token"}
	it, err := v.Create(dnsx.TokenItem("dns-cloudflare", conn, dnsserver.Token))
	if err != nil {
		t.Fatal(err)
	}
	conn.Credential = it.Ref()
	dm := dnsx.New(v)
	dm.Connections = func() []config.DNSConnection { return []config.DNSConnection{conn} }
	ca := acmeserver.New(t, dns)
	m := New(v, dm)
	m.Propagation = func(ctx context.Context, fqdn, value string, ns []string, log func(string)) error {
		log(fqdn + ": (test) skipping the propagation check")
		return nil
	}
	ws := &workspace.Workspace{Metadata: workspace.Meta{Name: "t"}, Path: dir, Resources: []*workspace.Resource{
		{Kind: "Certificate", Name: "web", Environment: "prod", Spec: map[string]any{"domains": []any{"example.com", "*.example.com"}, "ca": "custom", "directoryUrl": ca.URL, "email": "ops@example.com", "autoRenew": true}},
		{Kind: "Domain", Name: "shop", Spec: map[string]any{"zone": "example.com", "connection": "cloudflare"}},
	}}
	ws.Environments = []workspace.Environment{{Name: "prod", Protected: true}}
	return m, ws, ca, dns
}

func TestReadSpec(t *testing.T) {
	r := &workspace.Resource{Kind: "Certificate", Name: "x", Spec: map[string]any{"domains": []any{"Example.COM.", "www.example.com", "example.com"}}}
	s, err := ReadSpec(r)
	if err != nil || strings.Join(s.Domains, ",") != "example.com,www.example.com" || s.CA.ID != "letsencrypt" || s.KeyType != "ec256" || s.RenewDays != 30 {
		t.Fatalf("spec: %+v %v", s, err)
	}
	for _, bad := range []map[string]any{
		{"domains": []any{}},
		{"domains": []any{"not a domain"}},
		{"domains": []any{"a.example.com"}, "ca": "nope"},
		{"domains": []any{"a.example.com"}, "ca": "custom"},
		{"domains": []any{"*.example.com"}, "ca": "buypass"},
		{"domains": []any{"a.example.com"}, "keyType": "dsa"},
	} {
		if _, err := ReadSpec(&workspace.Resource{Kind: "Certificate", Name: "x", Spec: bad}); err == nil {
			t.Errorf("accepted %v", bad)
		}
	}
}

func TestIssueAndRenew(t *testing.T) {
	m, ws, ca, dns := env(t)
	ctx := context.Background()
	cert := ws.Resources[0]

	st := m.Status(ws, cert)
	if st.Issued || !st.Due || st.Reason != "not issued yet" || !st.Protected || st.CA != "custom" {
		t.Fatalf("status before: %+v", st)
	}

	var lines []string
	issued, err := m.Issue(ctx, ws, cert, func(s string) { lines = append(lines, s) })
	if err != nil {
		t.Fatalf("issue: %v\n%s", err, strings.Join(lines, "\n"))
	}
	log := strings.Join(lines, "\n")
	if issued.Item != "vault://t/cert-web" || issued.Info.Subject != "example.com" || strings.Join(issued.Info.Domains, ",") != "example.com,*.example.com" || issued.Info.Issuer != "Fake ACME Root" || issued.Info.Chain != 2 {
		t.Fatalf("issued: %+v", issued)
	}
	for _, want := range []string{"Created an account key", "Registered the account", "zone example.com at Cloudflare (connector cloudflare)", "created TXT _acme-challenge.example.com", "example.com: authorized", "removed TXT _acme-challenge.example.com", "Stored in the vault as vault://t/cert-web"} {
		if !strings.Contains(log, want) {
			t.Errorf("log lacks %q:\n%s", want, log)
		}
	}
	// two challenge records (the name and its wildcard), both removed
	if n := strings.Count(log, "created TXT"); n != 2 {
		t.Errorf("%d records created", n)
	}
	for _, rec := range dns.Zones["example.com"] {
		if strings.HasPrefix(rec["name"].(string), "_acme-challenge") {
			t.Fatalf("challenge record left: %v", rec)
		}
	}
	if ca.Finalized != 1 || len(ca.Accounts) != 1 {
		t.Fatalf("CA: %d finalized, %d accounts", ca.Finalized, len(ca.Accounts))
	}
	// the vault holds the chain, the key and readable metadata
	it, _, err := m.Vault.Resolve("vault://t/cert-web")
	if err != nil || it.Type != "certificate" || !strings.Contains(it.Get("certificate"), "BEGIN CERTIFICATE") || !strings.Contains(it.Get("private_key"), "EC PRIVATE KEY") || it.Get("issuer") != "Fake ACME Root" || !it.Policy.AgentUse {
		t.Fatalf("item: %+v %v", it, err)
	}
	for _, f := range it.Fields {
		if f.Name == "private_key" && !f.Secret {
			t.Fatal("key not secret")
		}
		if f.Name == "certificate" && f.Secret {
			t.Fatal("certificate marked secret")
		}
	}
	st = m.Status(ws, cert)
	if !st.Issued || st.Due || st.Info == nil || st.Info.DaysLeft < 88 || st.IssuedAt == "" {
		t.Fatalf("status after: %+v", st)
	}
	// a changed domain list makes it due
	cert.Spec["domains"] = []any{"example.com", "api.example.com"}
	if st := m.Status(ws, cert); !st.Due || st.Reason != "the domains changed" {
		t.Fatalf("changed domains: %+v", st)
	}
	cert.Spec["domains"] = []any{"example.com", "*.example.com"}

	// Renewing reuses the account and replaces the item in place.
	ca.Lifetime = 10 * 24 * time.Hour
	again, err := m.Issue(ctx, ws, cert, nil)
	if err != nil {
		t.Fatal(err)
	}
	if again.Item != issued.Item || again.Info.Serial == issued.Info.Serial || len(ca.Accounts) != 1 {
		t.Fatalf("renewal: %+v, accounts %d", again, len(ca.Accounts))
	}
	items, _ := m.Vault.List("t")
	n := 0
	for _, x := range items {
		if x.Name == "cert-web" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("%d cert items", n)
	}
	st = m.Status(ws, cert)
	if !st.Due || !strings.HasPrefix(st.Reason, "expires in") {
		t.Fatalf("short cert status: %+v", st)
	}

	// Jobs run in the background; RenewDue picks up due autoRenew certs.
	ca.Lifetime = 90 * 24 * time.Hour
	var recorded []string
	m.Record = func(wsName, actor, certName string, detail map[string]any, err error) {
		recorded = append(recorded, wsName+"/"+certName+"/"+actor)
	}
	jobs := m.RenewDue([]*workspace.Workspace{ws})
	if len(jobs) != 1 {
		t.Fatalf("%d renewals started", len(jobs))
	}
	if again := m.RenewDue([]*workspace.Workspace{ws}); len(again) != 0 {
		t.Fatal("a second renewal started while one runs")
	}
	wctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	v := jobs[0].Wait(wctx)
	if v.State != "done" || v.Result == nil || v.Result.Info.DaysLeft < 88 {
		t.Fatalf("job: %+v", v)
	}
	if len(recorded) != 1 || recorded[0] != "t/web/system" {
		t.Fatalf("recorded: %v", recorded)
	}
	if st := m.Status(ws, cert); st.Due {
		t.Fatalf("still due after renewal: %+v", st)
	}
	if j, ok := m.Job(v.ID); !ok || j.View(2).Total != v.Total || len(j.View(2).Lines) != v.Total-2 {
		t.Fatalf("job view: %v", ok)
	}
	if list := m.Jobs("t"); len(list) != 1 {
		t.Fatalf("jobs: %v", list)
	}
}

func TestIssueErrors(t *testing.T) {
	m, ws, ca, _ := env(t)
	ctx := context.Background()
	// a name the CA refuses
	r := &workspace.Resource{Kind: "Certificate", Name: "bad", Spec: map[string]any{"domains": []any{"shop.example.com", "x.example.invalid"}, "ca": "custom", "directoryUrl": ca.URL}}
	ws.Resources = append(ws.Resources, r)
	_, err := m.Issue(ctx, ws, r, nil)
	if err == nil || !strings.Contains(err.Error(), "no connected DNS account has a zone for x.example.invalid") {
		t.Fatalf("unknown zone: %v", err)
	}
	// without any connector
	m2 := New(m.Vault, dnsx.New(m.Vault))
	m2.Propagation = m.Propagation
	_, err = m2.Issue(ctx, ws, ws.Resources[0], nil)
	if err == nil || !strings.Contains(err.Error(), "not set up on this computer") {
		t.Fatalf("no connector: %v", err)
	}
	// nor a Domain resource naming one
	ws2 := &workspace.Workspace{Metadata: workspace.Meta{Name: "t"}, Path: ws.Path, Resources: ws.Resources[:1]}
	_, err = m2.Issue(ctx, ws2, ws2.Resources[0], nil)
	if err == nil || !strings.Contains(err.Error(), "connect Cloudflare or ArvanCloud") {
		t.Fatalf("no connector at all: %v", err)
	}
	// the challenge fails when the DNS fake does not see the record: a CA
	// without a DNS fake accepts everything, so point one at an unrelated
	// DNS fake
	other := dnsserver.ArvanCloud(t)
	ca3 := acmeserver.New(t, other)
	r3 := &workspace.Resource{Kind: "Certificate", Name: "fail", Spec: map[string]any{"domains": []any{"www.example.com"}, "ca": "custom", "directoryUrl": ca3.URL}}
	var lines []string
	_, err = m.Issue(ctx, ws, r3, func(s string) { lines = append(lines, s) })
	if err == nil || !strings.Contains(err.Error(), "validation failed") || !strings.Contains(err.Error(), "no TXT record") {
		t.Fatalf("failed validation: %v\n%s", err, strings.Join(lines, "\n"))
	}
	if !strings.Contains(strings.Join(lines, "\n"), "removed TXT _acme-challenge.www.example.com") {
		t.Fatalf("record not cleaned up after failure:\n%s", strings.Join(lines, "\n"))
	}
}

func TestInspectAndSplit(t *testing.T) {
	m, ws, _, _ := env(t)
	if _, err := m.Issue(context.Background(), ws, ws.Resources[0], nil); err != nil {
		t.Fatal(err)
	}
	chain, key, info, err := m.Material(ws, "web")
	if err != nil || info == nil || !strings.Contains(key, "PRIVATE KEY") {
		t.Fatalf("material: %v", err)
	}
	leaf, rest := SplitChain(chain)
	if strings.Count(leaf, "BEGIN CERTIFICATE") != 1 || strings.Count(rest, "BEGIN CERTIFICATE") != 1 {
		t.Fatalf("split: %d/%d", strings.Count(leaf, "BEGIN CERTIFICATE"), strings.Count(rest, "BEGIN CERTIFICATE"))
	}
	if _, err := Inspect("garbage"); err == nil {
		t.Fatal("inspected garbage")
	}
	if _, _, _, err := m.Material(ws, "nope"); err == nil || !strings.Contains(err.Error(), "not issued yet") {
		t.Fatalf("missing: %v", err)
	}
	o := DefaultPaths("*.example.com")
	if o.CertPath != "/etc/ssl/example.com/fullchain.pem" || o.Validate() != nil {
		t.Fatalf("defaults: %+v", o)
	}
	if err := (DeployOptions{CertPath: "relative.pem"}).Validate(); err == nil {
		t.Fatal("relative path accepted")
	}
	if err := (DeployOptions{}).Validate(); err == nil {
		t.Fatal("empty deploy accepted")
	}
}
