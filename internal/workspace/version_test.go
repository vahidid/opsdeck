package workspace

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func writeWS(t *testing.T, ws, resources string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "acme")
	os.MkdirAll(filepath.Join(dir, "resources"), 0o700)
	os.WriteFile(filepath.Join(dir, "workspace.yaml"), []byte(ws), 0o600)
	if resources != "" {
		os.WriteFile(filepath.Join(dir, "resources", "hosts.yaml"), []byte(resources), 0o600)
	}
	return dir
}

func TestParseAPIVersion(t *testing.T) {
	for in, want := range map[string]int{"": 1, "opsdeck/v1": 1, " opsdeck/v2 ": 2} {
		if got, err := parseAPIVersion(in); err != nil || got != want {
			t.Errorf("%q: got %d, %v; want %d", in, got, err, want)
		}
	}
	for _, in := range []string{"v1", "opsdeck/v0", "opsdeck/vx", "other/v1"} {
		if _, err := parseAPIVersion(in); err == nil {
			t.Errorf("%q: want an error", in)
		}
	}
}

func TestLoadRejectsNewerFormat(t *testing.T) {
	dir := writeWS(t, "apiVersion: opsdeck/v9\nkind: Workspace\nmetadata: {name: acme}\n", "")
	_, err := Load(dir)
	if !errors.Is(err, ErrNewerFormat) || !strings.Contains(err.Error(), "format 9") {
		t.Fatalf("want ErrNewerFormat, got %v", err)
	}
}

func TestLoadWithoutAPIVersion(t *testing.T) {
	dir := writeWS(t, "metadata: {name: acme}\n", "- {kind: Host, name: web-1, spec: {address: 10.0.0.1}}\n")
	w, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if w.Format != 1 || len(w.Resources) != 1 {
		t.Fatalf("format %d, %d resources", w.Format, len(w.Resources))
	}
}

func TestLoadEmptyResourceFile(t *testing.T) {
	dir := writeWS(t, "apiVersion: opsdeck/v1\nmetadata: {name: acme}\n", "\n")
	if _, err := Load(dir); err != nil {
		t.Fatal(err)
	}
}

// A future format change: format 2 renames Host spec.address to spec.host.
func TestMigrationRunsOnLoadAndSave(t *testing.T) {
	currentFormat = 2
	migrations[1] = func(ws *yaml.Node, res map[string]*yaml.Node) error {
		for _, doc := range res {
			var list []map[string]any
			if err := doc.Decode(&list); err != nil {
				return err
			}
			for _, r := range list {
				if spec, ok := r["spec"].(map[string]any); ok && r["kind"] == "Host" {
					spec["host"], spec["address"] = spec["address"], nil
					delete(spec, "address")
				}
			}
			if err := doc.Encode(list); err != nil {
				return err
			}
		}
		return nil
	}
	t.Cleanup(func() { currentFormat = FormatVersion; delete(migrations, 1) })

	dir := writeWS(t, "apiVersion: opsdeck/v1\nkind: Workspace\nmetadata: {name: acme}\n", "- {kind: Host, name: web-1, spec: {address: 10.0.0.1}}\n")
	w, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if w.Format != 1 || w.Resources[0].Str("host") != "10.0.0.1" || w.Resources[0].Str("address") != "" {
		t.Fatalf("not migrated: format %d, spec %v", w.Format, w.Resources[0].Spec)
	}
	// the files on disk stay as they were until the next save
	if b, _ := os.ReadFile(filepath.Join(dir, "workspace.yaml")); !strings.Contains(string(b), "opsdeck/v1") {
		t.Fatal("workspace.yaml was rewritten on load")
	}
	if err := w.Save(); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "workspace.yaml")); !strings.Contains(string(b), "apiVersion: opsdeck/v2") {
		t.Fatalf("saved as %s", b)
	}
}

func TestMigrationMissingStep(t *testing.T) {
	currentFormat = 3
	t.Cleanup(func() { currentFormat = FormatVersion })
	dir := writeWS(t, "apiVersion: opsdeck/v1\nmetadata: {name: acme}\n", "")
	if _, err := Load(dir); err == nil || !strings.Contains(err.Error(), "no migration from format 1") {
		t.Fatalf("want a missing-migration error, got %v", err)
	}
}

// Changing one resource of an old-format workspace upgrades the whole folder.
func TestMigrationResourceWriteUpgradesAll(t *testing.T) {
	currentFormat = 2
	migrations[1] = func(*yaml.Node, map[string]*yaml.Node) error { return nil }
	t.Cleanup(func() { currentFormat = FormatVersion; delete(migrations, 1) })
	dir := writeWS(t, "apiVersion: opsdeck/v1\nkind: Workspace\nmetadata: {name: acme}\n", "- {kind: Host, name: web-1, spec: {address: 10.0.0.1}}\n")
	w, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.PutResource("", &Resource{Kind: "Panel", Name: "grafana", Spec: map[string]any{"url": "http://x"}}); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "workspace.yaml")); !strings.Contains(string(b), "opsdeck/v2") {
		t.Fatalf("workspace.yaml not upgraded: %s", b)
	}
	w2, err := Load(dir)
	if err != nil || w2.Format != 2 || len(w2.Resources) != 2 {
		t.Fatalf("reload: %v, format %d, %d resources", err, w2.Format, len(w2.Resources))
	}
}
