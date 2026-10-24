package bundle

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"opsdeck/internal/vault"
	"opsdeck/internal/workspace"
)

// machine is one opsdeck install: a vault and a workspaces root.
func machine(t *testing.T, password string) (*workspace.Manager, *vault.Vault) {
	t.Helper()
	dir := t.TempDir()
	v, err := vault.Open(filepath.Join(dir, "vault.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { v.Close() })
	if err := v.Init(password); err != nil {
		t.Fatal(err)
	}
	os.MkdirAll(filepath.Join(dir, "workspaces"), 0o700)
	return workspace.NewManager(filepath.Join(dir, "workspaces"), nil, nil), v
}

func TestExportImport(t *testing.T) {
	mA, vA := machine(t, "alice master pw")
	ws, err := mA.Create(workspace.Meta{Name: "acme", Title: "ACME"})
	if err != nil {
		t.Fatal(err)
	}
	if err := ws.PutResource("", &workspace.Resource{Kind: "Host", Name: "app-1", Spec: map[string]any{"address": "10.0.0.5", "auth": "vault://acme/deploy"}}); err != nil {
		t.Fatal(err)
	}
	for rel, content := range map[string]string{
		"docs/runbook.md":            "# Runbook",
		".opsdeck/sessions/s1.json":  `{"messages":[]}`,
		"reports/2026/10/audit.md":   "report",
		"repos/deploy/README.md":     "clone",
		"artifacts/2026-10-21/x.txt": "artifact",
	} {
		p := filepath.Join(ws.Path, filepath.FromSlash(rel))
		os.MkdirAll(filepath.Dir(p), 0o700)
		os.WriteFile(p, []byte(content), 0o600)
	}
	vA.EnsureScope("acme")
	if _, err := vA.Create(vault.Item{Scope: "acme", Type: "login", Name: "deploy", Fields: []vault.Field{{Name: "username", Value: "deploy"}, {Name: "password", Value: "hunter2-secret"}}, Policy: vault.Policy{AgentUse: true}}); err != nil {
		t.Fatal(err)
	}
	if _, err := vA.Create(vault.Item{Scope: vault.GlobalScope, Type: "token", Name: "personal", Fields: []vault.Field{{Name: "token", Value: "not-shared"}}}); err != nil {
		t.Fatal(err)
	}

	f, n, err := Export(ws, vA, "file password")
	if err != nil || n != 1 {
		t.Fatalf("export: %v, %d items", err, n)
	}
	raw, _ := json.Marshal(f)
	var got File
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}

	mB, vB := machine(t, "bob master pw")
	if _, err := Import(mB, vB, &got, "wrong password"); !errors.Is(err, vault.ErrBadFilePassword) {
		t.Fatalf("wrong password: %v", err)
	}
	res, err := Import(mB, vB, &got, "file password")
	if err != nil {
		t.Fatal(err)
	}
	if res.Items != 1 || res.Skipped != 0 {
		t.Fatalf("items %d skipped %d", res.Items, res.Skipped)
	}
	wsB, err := mB.Get("acme")
	if err != nil || wsB.Find("Host", "app-1") == nil || wsB.Metadata.Title != "ACME" {
		t.Fatalf("imported workspace: %v %+v", err, wsB)
	}
	if b, _ := os.ReadFile(filepath.Join(wsB.Path, "docs", "runbook.md")); string(b) != "# Runbook" {
		t.Fatalf("docs not imported: %q", b)
	}
	for _, rel := range []string{".opsdeck/sessions", "reports", "repos", "artifacts"} {
		if _, err := os.Stat(filepath.Join(wsB.Path, rel)); err == nil {
			t.Errorf("%s should not be exported", rel)
		}
	}

	// the secrets now live under bob's vault and password
	vB.Lock()
	if err := vB.Unlock("bob master pw"); err != nil {
		t.Fatal(err)
	}
	it, _, err := vB.ResolveFor("acme", "vault://acme/deploy")
	if err != nil || it.Get("password") != "hunter2-secret" || !it.Policy.AgentUse {
		t.Fatalf("imported item: %v %+v", err, it)
	}
	if global, _ := vB.List(vault.GlobalScope); len(global) != 0 {
		t.Fatalf("global items must not be exported: %+v", global)
	}

	if _, err := Import(mB, vB, &got, "file password"); err == nil {
		t.Fatal("importing over an existing workspace should fail")
	}
	if items, _ := vB.List("acme"); len(items) != 1 {
		t.Fatalf("a failed import must not add items: %d", len(items))
	}
}

func TestCreateFromFilesRejectsPaths(t *testing.T) {
	m, _ := machine(t, "some master pw")
	for _, rel := range []string{"../evil", "/etc/passwd", "docs/../../evil", ".opsdeck/known_hosts"} {
		files := map[string][]byte{"workspace.yaml": []byte("metadata:\n  name: acme\n"), rel: []byte("x")}
		if _, err := m.CreateFromFiles("acme", files); err == nil {
			t.Errorf("%s: accepted", rel)
		}
	}
}
