package vault

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newVault(t *testing.T) (*Vault, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "vault.db")
	v, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { v.Close() })
	return v, path
}

func TestLifecycle(t *testing.T) {
	v, path := newVault(t)
	if v.Initialized() {
		t.Fatal("new vault should not be initialized")
	}
	if err := v.Init("correct horse battery"); err != nil {
		t.Fatal(err)
	}
	if err := v.EnsureScope("acme"); err != nil {
		t.Fatal(err)
	}
	it, err := v.Create(Item{Scope: "acme", Type: "login", Name: "db-admin", Fields: []Field{
		{Name: "username", Value: "root"}, {Name: "password", Value: "s3cr3t-value"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if !it.Fields[1].Secret {
		t.Fatal("password must be marked secret by type definition")
	}
	if _, err := v.Create(Item{Scope: "acme", Type: "login", Name: "DB-ADMIN"}); err != ErrConflict {
		t.Fatalf("want conflict, got %v", err)
	}

	if st, _ := os.Stat(path); st.Mode().Perm() != 0o600 {
		t.Fatalf("vault file mode %v", st.Mode().Perm())
	}
	// Nothing readable on disk.
	db, _ := sql.Open("sqlite", path)
	var blob []byte
	if err := db.QueryRow(`SELECT blob FROM items`).Scan(&blob); err != nil {
		t.Fatal(err)
	}
	db.Close()
	if strings.Contains(string(blob), "db-admin") || strings.Contains(string(blob), "s3cr3t") {
		t.Fatal("plaintext leaked to disk")
	}

	v.Lock()
	if _, err := v.List("acme"); err != ErrLocked {
		t.Fatalf("want locked, got %v", err)
	}
	if err := v.Unlock("wrong password"); err != ErrBadPassword {
		t.Fatalf("want bad password, got %v", err)
	}

	// Reopen from disk.
	v.Close()
	v2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer v2.Close()
	if err := v2.Unlock("correct horse battery"); err != nil {
		t.Fatal(err)
	}
	got, field, err := v2.Resolve("vault://acme/db-admin#password")
	if err != nil {
		t.Fatal(err)
	}
	if field != "password" || got.Get("password") != "s3cr3t-value" {
		t.Fatalf("resolve mismatch: %q %q", field, got.Get("password"))
	}

	// Update keeping secret.
	upd, err := v2.Update(got.ID, Item{Name: "db-admin", Fields: []Field{
		{Name: "username", Value: "admin"}, {Name: "password"},
	}}, map[string]bool{"password": true})
	if err != nil {
		t.Fatal(err)
	}
	if upd.Get("password") != "s3cr3t-value" || upd.Get("username") != "admin" || upd.Revision != 2 {
		t.Fatalf("update mismatch: %+v", upd)
	}

	if err := v2.ChangePassword("correct horse battery", "new password 123"); err != nil {
		t.Fatal(err)
	}
	v2.Lock()
	if err := v2.Unlock("new password 123"); err != nil {
		t.Fatal(err)
	}
	if err := v2.DeleteScope("acme"); err != nil {
		t.Fatal(err)
	}
	if items, _ := v2.List(""); len(items) != 0 {
		t.Fatalf("scope delete left %d items", len(items))
	}
}

func TestSSHKeys(t *testing.T) {
	for _, alg := range []string{"ed25519", "ecdsa", "rsa"} {
		fields, err := GenerateSSHKey(alg, "test@opsdeck")
		if err != nil {
			t.Fatal(alg, err)
		}
		it := Item{Type: "ssh-key", Name: "k", Fields: fields}
		if _, err := Signer(it); err != nil {
			t.Fatal(alg, err)
		}
		if _, fp := PublicKeyInfo(it); !strings.HasPrefix(fp, "SHA256:") {
			t.Fatal(alg, "bad fingerprint", fp)
		}
		if _, err := ImportSSHKey(it.Get("private_key"), ""); err != nil {
			t.Fatal(alg, err)
		}
	}
}

func TestTOTP(t *testing.T) {
	// RFC 6238 test vector (SHA1, secret "12345678901234567890").
	code, _, _, err := TOTP("GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ", time.Unix(59, 0))
	if err != nil {
		t.Fatal(err)
	}
	if code != "287082" {
		t.Fatalf("got %s", code)
	}
}

func TestScopesAndSecrecy(t *testing.T) {
	v, _ := newVault(t)
	if err := v.Init("password123"); err != nil {
		t.Fatal(err)
	}
	v.EnsureScope("a")
	v.EnsureScope("b")
	it, err := v.Create(Item{Scope: "b", Type: "note", Name: "n", Fields: []Field{{Name: "content", Value: "x"}, {Name: "api_secret", Value: "custom-secret", Secret: true}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := v.ResolveFor("a", "vault://b/n"); !errors.Is(err, ErrScope) {
		t.Fatalf("cross-workspace reference must fail: %v", err)
	}
	if _, _, err := v.ResolveFor("b", "vault://b/n"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := v.ResolveFor("", "vault://b/n"); !errors.Is(err, ErrScope) {
		t.Fatal("app-level refs must be global")
	}
	upd, err := v.Update(it.ID, Item{Name: "n", Fields: []Field{{Name: "content"}, {Name: "api_secret", Secret: false}}},
		map[string]bool{"content": true, "api_secret": true})
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range upd.Fields {
		if f.Name == "api_secret" && (!f.Secret || f.Value != "custom-secret") {
			t.Fatalf("kept secret downgraded: %+v", f)
		}
	}
}
