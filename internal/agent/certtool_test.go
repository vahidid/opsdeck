package agent

import (
	"context"
	"strings"
	"testing"

	"opsdeck/internal/acmex"
	"opsdeck/internal/testutil/acmeserver"
	"opsdeck/internal/workspace"
)

// TestCertificateTool: list, issue (a change that runs the whole dns-01
// flow against the fake CA), show, and a deploy call's approval card.
func TestCertificateTool(t *testing.T) {
	env, f := dnsEnv(t)
	ca := acmeserver.New(t, f)
	env.ACME = acmex.New(env.Vault, env.DNS)
	env.ACME.Propagation = func(ctx context.Context, fqdn, value string, ns []string, log func(string)) error { return nil }
	env.WS.Resources = append(env.WS.Resources, &workspace.Resource{Kind: "Certificate", Name: "web", Environment: "prod",
		Spec: map[string]any{"domains": []any{"example.com", "www.example.com"}, "ca": "custom", "directoryUrl": ca.URL, "email": "ops@example.com"}})
	tl := toolCertificate()

	out, _ := viewOf(t, prepare(t, tl, env, map[string]any{"action": "list"}))
	if !strings.Contains(out, "- web: example.com, www.example.com from Another ACME CA env=prod (PROTECTED) — not issued yet") {
		t.Fatalf("list: %s", out)
	}
	// deploy before issuing is refused
	if _, err := tl.Prepare(context.Background(), env, raw(map[string]any{"action": "deploy", "host": "web-1"})); err == nil || !strings.Contains(err.Error(), "not issued yet") {
		t.Fatalf("deploy unissued: %v", err)
	}

	c := prepare(t, tl, env, map[string]any{"action": "issue"})
	if c.Risk != RiskChange || c.Target != "certificate/web" || !c.Protected || c.Match != "issue example.com www.example.com" || !strings.Contains(c.Preview, "issue the certificate web for example.com, www.example.com from Another ACME CA") ||
		strings.Join(c.Credentials, ",") != "vault://global/dns-cloudflare" {
		t.Fatalf("issue call: %+v", c)
	}
	out, _ = viewOf(t, c)
	if !strings.Contains(out, "Stored in the vault as vault://t/cert-web") || !strings.Contains(out, "Deploy it where it is used") || strings.Contains(out, "PRIVATE KEY") {
		t.Fatalf("issue: %s", out)
	}
	if ca.Finalized != 1 {
		t.Fatalf("finalized %d", ca.Finalized)
	}

	out, _ = viewOf(t, prepare(t, tl, env, map[string]any{"action": "show"}))
	if !strings.Contains(out, "issuer: Fake ACME Root") || !strings.Contains(out, "names: example.com, www.example.com") || !strings.Contains(out, "BEGIN CERTIFICATE") || strings.Contains(out, "PRIVATE KEY") {
		t.Fatalf("show: %s", out)
	}
	out, _ = viewOf(t, prepare(t, tl, env, map[string]any{"action": "list"}))
	if !strings.Contains(out, "issued by Fake ACME Root") || strings.Contains(out, "DUE") {
		t.Fatalf("list after: %s", out)
	}
	// renewing is named as such
	if c := prepare(t, tl, env, map[string]any{"action": "issue", "certificate": "web"}); c.Match != "renew example.com www.example.com" || !strings.Contains(c.Preview, "valid for") {
		t.Fatalf("renew call: %+v", c)
	}

	// deploy: a change on the host with the files on the card and the
	// certificate item among the credentials
	c = prepare(t, tl, env, map[string]any{"action": "deploy", "host": "web-1", "cert_path": "/etc/nginx/ssl/fullchain.pem", "key_path": "/etc/nginx/ssl/privkey.pem"})
	if c.Risk != RiskChange || c.Target != "host/web-1" || !strings.Contains(c.Preview, "/etc/nginx/ssl/privkey.pem  ← private key (0600)") || !strings.Contains(c.Match, "deploy web /etc/nginx/ssl/fullchain.pem") ||
		!strings.Contains(strings.Join(c.Credentials, ","), "vault://t/cert-web") || !c.Protected {
		t.Fatalf("deploy call: %+v", c)
	}
	// default paths from the first domain
	if c := prepare(t, tl, env, map[string]any{"action": "deploy", "host": "web-1"}); !strings.Contains(c.Preview, "/etc/ssl/example.com/fullchain.pem") {
		t.Fatalf("default paths: %s", c.Preview)
	}
	for _, bad := range []map[string]any{
		{"action": "deploy", "host": "nope"},
		{"action": "deploy", "host": "web-1", "cert_path": "relative.pem"},
		{"action": "show", "certificate": "nope"},
		{"action": "revoke"},
	} {
		if _, err := tl.Prepare(context.Background(), env, raw(bad)); err == nil {
			t.Errorf("accepted %v", bad)
		}
	}
	// offered only with a Certificate
	names := map[string]bool{}
	for _, tl := range availableTools(env.WS, "", false) {
		names[tl.Def.Name] = true
	}
	if !names["certificate"] {
		t.Fatal("certificate tool not offered")
	}
}
