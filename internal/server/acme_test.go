package server

import (
	"context"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"opsdeck/internal/paths"
	"opsdeck/internal/testutil/acmeserver"
	"opsdeck/internal/testutil/dnsserver"
	"opsdeck/internal/testutil/sshd"
)

// TestCertificates: a Certificate resource is issued from a fake CA through
// a fake Cloudflare account (dns-01), followed as a job, shown with its
// chain, deployed to an SSH host (key 0600) with a command run afterwards,
// and everything is audited.
func TestCertificates(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("OPSDECK_HOME", t.TempDir())
	srv, err := New(paths.Default(), Options{Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	srv.acme.Propagation = func(ctx context.Context, fqdn, value string, ns []string, log func(string)) error { return nil }
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	jar, _ := cookiejar.New(nil)
	c := &client{t: t, base: ts.URL, http: &http.Client{Jar: jar, Timeout: 60 * time.Second}}
	c.must("POST", "/api/auth/login", map[string]string{"token": srv.token}, nil)
	c.must("POST", "/api/vault/init", map[string]string{"password": "correct horse battery"}, nil)
	c.must("POST", "/api/workspaces", map[string]string{"name": "acme"}, nil)

	dns := dnsserver.Cloudflare(t)
	ca := acmeserver.New(t, dns)
	c.must("POST", "/api/dns/connections", map[string]any{"provider": "cloudflare", "apiUrl": dns.URL, "token": dnsserver.Token}, nil)

	var cas []map[string]any
	c.must("GET", "/api/acme/cas", nil, &cas)
	if len(cas) < 3 || cas[0]["id"] != "letsencrypt" {
		t.Fatalf("cas: %v", cas)
	}

	// An invalid spec is reported, not accepted for issuance.
	c.must("POST", "/api/workspaces/acme/resources", map[string]any{"kind": "Certificate", "name": "bad", "spec": map[string]any{"domains": []any{"x.example.com"}, "ca": "custom"}}, nil)
	var list []map[string]any
	c.must("GET", "/api/workspaces/acme/certificates", nil, &list)
	if len(list) != 1 || !strings.Contains(list[0]["error"].(string), "directoryUrl") {
		t.Fatalf("bad spec: %v", list)
	}
	if msg := failure(c, "POST", "/api/workspaces/acme/certificates/bad/issue", nil); !strings.Contains(msg, "directoryUrl") {
		t.Fatalf("issue bad: %v", msg)
	}
	c.must("DELETE", "/api/workspaces/acme/resources/Certificate/bad", nil, nil)

	c.must("POST", "/api/workspaces/acme/resources", map[string]any{"kind": "Certificate", "name": "web", "environment": "prod",
		"spec": map[string]any{"domains": []any{"example.com", "www.example.com"}, "ca": "custom", "directoryUrl": ca.URL, "email": "ops@example.com", "renewDays": 20}}, nil)
	c.must("GET", "/api/workspaces/acme/certificates", nil, &list)
	if len(list) != 1 || list[0]["issued"] != false || list[0]["due"] != true || list[0]["reason"] != "not issued yet" || list[0]["item"] != "vault://acme/cert-web" {
		t.Fatalf("before: %v", list)
	}

	// Issue: a job to follow.
	var job map[string]any
	c.must("POST", "/api/workspaces/acme/certificates/web/issue", nil, &job)
	id := job["id"].(string)
	// starting again while it runs returns the same job
	var again map[string]any
	c.must("POST", "/api/workspaces/acme/certificates/web/issue", nil, &again)
	if again["id"] != id {
		t.Fatalf("second issue started another job: %v", again)
	}
	for i := 0; i < 60 && job["state"] == "running"; i++ {
		c.must("GET", "/api/workspaces/acme/certificates/jobs/"+id+"?wait=1", nil, &job)
	}
	lines := strings.Join(toStrings(job["lines"]), "\n")
	if job["state"] != "done" {
		t.Fatalf("job: %v\n%s", job, lines)
	}
	if !strings.Contains(lines, "created TXT _acme-challenge.example.com") || !strings.Contains(lines, "www.example.com: authorized") || !strings.Contains(lines, "Stored in the vault") {
		t.Fatalf("log:\n%s", lines)
	}
	var tail map[string]any
	c.must("GET", "/api/workspaces/acme/certificates/jobs/"+id+"?since=3", nil, &tail)
	if len(toStrings(tail["lines"])) != len(toStrings(job["lines"]))-3 {
		t.Fatalf("since: %v", tail)
	}
	var jobs []map[string]any
	c.must("GET", "/api/workspaces/acme/certificates/jobs", nil, &jobs)
	if len(jobs) != 1 || jobs[0]["cert"] != "web" {
		t.Fatalf("jobs: %v", jobs)
	}

	var detail map[string]any
	c.must("GET", "/api/workspaces/acme/certificates/web", nil, &detail)
	info := detail["info"].(map[string]any)
	if detail["issued"] != true || detail["due"] != false || info["issuer"] != "Fake ACME Root" || !strings.Contains(detail["chain"].(string), "BEGIN CERTIFICATE") || strings.Contains(detail["chain"].(string), "PRIVATE") {
		t.Fatalf("detail: %v", detail)
	}
	// the key is a secret field of the vault item: not listed
	var items []map[string]any
	c.must("GET", "/api/vault/items?scope=acme", nil, &items)
	found := false
	for _, it := range items {
		if it["name"] == "cert-web" {
			found = true
			for _, f := range it["fields"].([]any) {
				fm := f.(map[string]any)
				if fm["name"] == "private_key" && fm["value"] != nil && fm["value"] != "" {
					t.Fatal("private key listed")
				}
			}
		}
	}
	if !found {
		t.Fatal("no cert-web item")
	}

	// Deploy to a host.
	var key map[string]any
	c.must("POST", "/api/vault/ssh/generate", map[string]any{"scope": "acme", "name": "k", "algorithm": "ed25519"}, &key)
	pub, _, _, _, _ := ssh.ParseAuthorizedKey([]byte(key["publicKey"].(string)))
	workdir := t.TempDir()
	host, err := sshd.Start("127.0.0.1:0", sshd.Options{AuthorizedKeys: []ssh.PublicKey{pub}, Dir: workdir})
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close()
	_, port, _ := net.SplitHostPort(host.Addr)
	p, _ := strconv.Atoi(port)
	c.must("POST", "/api/workspaces/acme/resources", map[string]any{"kind": "Host", "name": "web-1", "spec": map[string]any{"address": "127.0.0.1", "port": p, "auth": "vault://acme/k"}}, nil)
	dir := filepath.Join(workdir, "ssl")
	var dep map[string]any
	c.must("POST", "/api/workspaces/acme/certificates/web/deploy", map[string]any{"host": "web-1", "certPath": dir + "/fullchain.pem", "keyPath": dir + "/privkey.pem", "bundlePath": dir + "/haproxy.pem",
		"command": "cat " + dir + "/fullchain.pem | head -1"}, &dep)
	if len(toStrings(dep["written"])) != 3 || dep["exitCode"] != 0.0 || !strings.Contains(dep["stdout"].(string), "BEGIN CERTIFICATE") {
		t.Fatalf("deploy: %v", dep)
	}
	st, err := os.Stat(dir + "/privkey.pem")
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("key mode: %v %v", st, err)
	}
	if st, _ := os.Stat(dir + "/fullchain.pem"); st.Mode().Perm() != 0o644 {
		t.Fatalf("chain mode: %v", st.Mode())
	}
	bundle, _ := os.ReadFile(dir + "/haproxy.pem")
	if !strings.Contains(string(bundle), "BEGIN CERTIFICATE") || !strings.Contains(string(bundle), "PRIVATE KEY") {
		t.Fatal("bundle lacks chain or key")
	}
	if code := c.do("POST", "/api/workspaces/acme/certificates/web/deploy", map[string]any{"host": "web-1", "certPath": "relative.pem"}, nil); code != 400 {
		t.Fatalf("relative path: %d", code)
	}
	if code := c.do("POST", "/api/workspaces/acme/certificates/web/deploy", map[string]any{"host": "web-1", "certPath": dir + "/x.pem", "command": "exit 3"}, &dep); code != 502 || dep["exitCode"] != 3.0 {
		t.Fatalf("failing command: %d %v", code, dep)
	}

	var audit []map[string]any
	c.must("GET", "/api/audit?limit=50", nil, &audit)
	actions := map[string]int{}
	for _, e := range audit {
		if e["result"] != "error" {
			actions[e["action"].(string)]++
		}
	}
	if actions["certificate.issue"] != 2 || actions["certificate.issued"] != 1 || actions["certificate.deploy"] != 1 {
		t.Fatalf("audit: %v", actions)
	}
}

func toStrings(v any) []string {
	var out []string
	if list, ok := v.([]any); ok {
		for _, x := range list {
			out = append(out, x.(string))
		}
	}
	return out
}
