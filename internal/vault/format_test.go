package vault

import (
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
)

func TestOpenRefusesNewerFormat(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vault.db")
	v, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := v.Init("correct horse battery"); err != nil {
		t.Fatal(err)
	}
	v.Close()
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE meta SET v=? WHERE k='format_version'`, []byte("2")); err != nil {
		t.Fatal(err)
	}
	db.Close()
	if _, err := Open(path); !errors.Is(err, ErrNewerFormat) {
		t.Fatalf("want ErrNewerFormat, got %v", err)
	}
}
