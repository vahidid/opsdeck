package connect

import (
	"crypto/ed25519"
	"crypto/rand"
	"os"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"

	"opsdeck/internal/workspace"
)

func TestKnownHostsImport(t *testing.T) {
	dir := t.TempDir()
	h := NewHub(nil, func(string) string { return dir })
	w := &workspace.Workspace{Metadata: workspace.Meta{Name: "acme"}}
	key := func() ssh.PublicKey {
		p, _, _ := ed25519.GenerateKey(rand.Reader)
		k, _ := ssh.NewPublicKey(p)
		return k
	}
	kA, kB, kC, kOld := key(), key(), key(), key()
	host := func(name, addr string, port int) *workspace.Resource {
		return &workspace.Resource{Kind: "Host", Name: name, Spec: map[string]any{"address": addr, "port": port}}
	}
	a, b, c, d := host("a", "10.0.0.1", 22), host("b", "db.internal", 2222), host("c", "10.0.0.3", 22), host("d", "10.9.9.9", 22)
	file := knownhosts.Line([]string{knownhosts.HashHostname("10.0.0.1")}, kA) + "\n" +
		"bad line\n" +
		"*.internal,!web.internal " + strings.TrimSpace(string(ssh.MarshalAuthorizedKey(kB))) + "\n" + // wildcard does not match a port
		knownhosts.Line([]string{"[db.internal]:2222"}, kB) + "\n" +
		"@revoked 10.0.0.3 " + strings.TrimSpace(string(ssh.MarshalAuthorizedKey(kC))) + "\n" +
		knownhosts.Line([]string{"10.0.0.3"}, kC) + "\n"

	// c already has a different pinned key: conflict
	cb := h.hostKeyCallback("acme")
	if err := cb("10.0.0.3:22", nil, kOld); err != nil {
		t.Fatal(err)
	}
	plan := h.PlanKnownHostsImport(w, []*workspace.Resource{a, b, c, d}, []byte(file))
	got := map[string]string{}
	for _, p := range plan {
		got[p.Host] = p.Status
	}
	if got["a"] != "add" || got["b"] != "add" || got["c"] != "conflict" || got["d"] != "not-found" {
		t.Fatalf("plan: %v", got)
	}
	n, err := h.ApplyKnownHostsImport(w, plan)
	if err != nil || n != 2 {
		t.Fatalf("applied %d: %v", n, err)
	}
	if err := cb("10.0.0.1:22", nil, kA); err != nil {
		t.Fatalf("imported key not accepted: %v", err)
	}
	if err := cb("[db.internal]:2222", nil, kA); err == nil {
		t.Fatal("other key accepted for db.internal")
	}
	if err := cb("10.0.0.3:22", nil, kC); err == nil {
		t.Fatal("conflicting host must keep its pinned key")
	}
	// importing again changes nothing
	plan = h.PlanKnownHostsImport(w, []*workspace.Resource{a}, []byte(file))
	if plan[0].Status != "pinned" {
		t.Fatalf("second import: %v", plan[0].Status)
	}
	b2, _ := os.ReadFile(h.knownHostsFile("acme"))
	if strings.Count(string(b2), "\n") != 3 {
		t.Fatalf("known_hosts:\n%s", b2)
	}
}

func TestWildcardMatch(t *testing.T) {
	for _, tc := range []struct {
		p, s string
		ok   bool
	}{{"*.example.com", "a.example.com", true}, {"*.example.com", "example.com", false}, {"10.0.0.?", "10.0.0.7", true},
		{"10.0.0.?", "10.0.0.17", false}, {"WEB*", "web-1", true}, {"*", "", true}} {
		if wildcardMatch(tc.p, tc.s) != tc.ok {
			t.Errorf("%q ~ %q: want %v", tc.p, tc.s, tc.ok)
		}
	}
	if matchKnownHost([]string{"*.internal", "!web.internal"}, "web.internal") {
		t.Error("negation ignored")
	}
}
