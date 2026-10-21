package workspace

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestManagerAndValidation(t *testing.T) {
	root := t.TempDir()
	m := NewManager(root, nil, nil)
	if _, err := m.Create(Meta{Name: "Bad Name"}); err == nil {
		t.Fatal("invalid name accepted")
	}
	w, err := m.Create(Meta{Name: "acme", Title: "ACME"})
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"workspace.yaml", "OPSDECK.md", "docs/README.md"} {
		if _, err := os.Stat(filepath.Join(w.Path, f)); err != nil {
			t.Fatalf("missing %s", f)
		}
	}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(w.PutResource("", &Resource{Kind: "WireGuard", Name: "office", Spec: map[string]any{"config": "vault://acme/wg"}}))
	must(w.PutResource("", &Resource{Kind: "Host", Name: "bastion", Via: "network/office", Spec: map[string]any{"address": "10.0.0.1", "auth": "vault://acme/key"}}))
	must(w.PutResource("", &Resource{Kind: "Host", Name: "app-1", Environment: "prod", Via: "host/bastion", Spec: map[string]any{"address": "10.0.1.5", "roles": []any{"app"}}}))
	must(w.PutResource("", &Resource{Kind: "HostGroup", Name: "apps", Spec: map[string]any{"roles": []any{"app"}}}))
	if err := w.PutResource("", &Resource{Kind: "Host", Name: "app-1"}); err == nil {
		t.Fatal("duplicate accepted")
	}

	// Reload from disk and check the chain.
	w, err = m.Get("acme")
	must(err)
	chain, err := w.Chain(w.Find("Host", "app-1"))
	must(err)
	if len(chain) != 2 || chain[0].ID() != "WireGuard/office" || chain[1].ID() != "Host/bastion" {
		t.Fatalf("chain: %v", chain)
	}
	if members := w.GroupMembers(w.Find("HostGroup", "apps")); len(members) != 1 {
		t.Fatalf("group members: %d", len(members))
	}
	if w.FindTarget("group/apps") == nil || w.FindTarget("app-1") == nil {
		t.Fatal("FindTarget")
	}

	issues := w.Validate(func(ref string) error {
		if ref == "vault://acme/key" {
			return nil
		}
		return os.ErrNotExist
	})
	text := ""
	for _, is := range issues {
		text += is.Level + " " + is.Resource + " " + is.Message + "\n"
	}
	if !strings.Contains(text, "WireGuard/office") || !strings.Contains(text, "warning Host/app-1 no credential") {
		t.Fatalf("issues:\n%s", text)
	}

	// A via loop is reported.
	w.Find("Host", "bastion").Via = "host/app-1"
	if _, err := w.Chain(w.Find("Host", "app-1")); err == nil || !strings.Contains(err.Error(), "loop") {
		t.Fatalf("loop not detected: %v", err)
	}

	// Literal secrets in secretRef fields are an error.
	w.Find("Host", "bastion").Via = ""
	w.Find("Host", "bastion").Spec["auth"] = "hunter2"
	found := false
	for _, is := range w.Validate(nil) {
		found = found || strings.Contains(is.Message, "never a literal secret")
	}
	if !found {
		t.Fatal("literal secret not reported")
	}

	// Docs path safety.
	if err := w.WriteDoc("../escape.md", "x"); err == nil {
		t.Fatal("path traversal accepted")
	}
	must(w.WriteDoc("docs/runbooks/deploy.md", "# Deploy\n"))
	must(w.WriteDoc("skills/restart.md", "---\nname: restart-app\ndescription: Restart the app safely\n---\nsteps"))
	if sk := w.Skills(); len(sk) != 1 || sk[0].Name != "restart-app" {
		t.Fatalf("skills: %+v", sk)
	}
	// Symlinked docs may not escape the workspace.
	outside := t.TempDir()
	os.WriteFile(filepath.Join(outside, "secret.md"), []byte("token"), 0o600)
	must(os.Symlink(outside, filepath.Join(w.Path, "docs", "link")))
	if _, err := w.ReadDoc("docs/link/secret.md"); err == nil {
		t.Fatal("symlink escape allowed")
	}
	// Invalid names on disk are rejected.
	bad := filepath.Join(root, "bad")
	os.MkdirAll(bad, 0o700)
	os.WriteFile(filepath.Join(bad, "workspace.yaml"), []byte("metadata:\n  name: ../../etc\n"), 0o600)
	if _, err := Load(bad); err == nil {
		t.Fatal("invalid name accepted")
	}
	os.WriteFile(filepath.Join(bad, "workspace.yaml"), []byte("metadata:\n  name: global\n"), 0o600)
	if _, err := Load(bad); err == nil {
		t.Fatal("reserved name accepted")
	}
	os.RemoveAll(bad)
	must(m.Delete("acme"))
	if _, err := m.Get("acme"); err == nil {
		t.Fatal("not deleted")
	}
}
