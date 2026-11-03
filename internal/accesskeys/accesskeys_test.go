package accesskeys

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keys.json")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Create("x", "acme", "full"); err == nil {
		t.Fatal("mode full must be refused")
	}
	k, secret, err := s.Create("Claude Code", "acme", "ask")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(secret, Prefix) || k.Hash != "" || !strings.HasPrefix(secret, k.Hint) {
		t.Fatalf("key %+v secret %q", k, secret)
	}
	b, _ := os.ReadFile(path)
	if strings.Contains(string(b), secret) {
		t.Fatal("the secret is stored in clear")
	}
	if st, _ := os.Stat(path); st.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", st.Mode())
	}
	// survives a reload
	s2, _ := Open(path)
	got, ok := s2.Lookup(secret)
	if !ok || got.ID != k.ID || got.LastUsed.IsZero() {
		t.Fatalf("lookup: %+v %v", got, ok)
	}
	if _, ok := s2.Lookup(secret + "0"); ok {
		t.Fatal("a wrong secret matched")
	}
	if _, ok := s2.Lookup("nope"); ok {
		t.Fatal("a secret without the prefix matched")
	}
	if u, err := s2.Update(k.ID, "", "plan"); err != nil || u.Mode != "plan" || u.Label != "Claude Code" {
		t.Fatalf("update: %+v %v", u, err)
	}
	if err := s2.Revoke(k.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok := s2.Lookup(secret); ok {
		t.Fatal("a revoked key still works")
	}
}

func TestRevokeWorkspace(t *testing.T) {
	s, _ := Open(filepath.Join(t.TempDir(), "keys.json"))
	s.Create("a", "acme", "ask")
	s.Create("b", "other", "plan")
	s.RevokeWorkspace("acme")
	if l := s.List(); len(l) != 1 || l[0].Workspace != "other" {
		t.Fatalf("%+v", l)
	}
}

func TestRotate(t *testing.T) {
	s, _ := Open(filepath.Join(t.TempDir(), "keys.json"))
	k, old, err := s.Create("Claude Code", "acme", "ask")
	if err != nil {
		t.Fatal(err)
	}
	k2, secret, err := s.Rotate(k.ID)
	if err != nil || secret == old || k2.ID != k.ID || k2.Rotated.IsZero() {
		t.Fatalf("%v %v", k2, err)
	}
	if _, ok := s.Lookup(old); ok {
		t.Fatal("old secret still works")
	}
	if got, ok := s.Lookup(secret); !ok || got.ID != k.ID {
		t.Fatal("new secret refused")
	}
	if _, err := s.SetClient(k.ID, "cursor", "opsdeck-acme"); err != nil {
		t.Fatal(err)
	}
	s2, _ := Open(s.path)
	if g, _ := s2.Get(k.ID); g.Client != "cursor" || g.Server != "opsdeck-acme" || g.Hash != "" {
		t.Fatalf("%+v", g)
	}
}
