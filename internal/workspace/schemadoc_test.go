package workspace

import (
	"os"
	"testing"
)

func TestSchemaDocIsCurrent(t *testing.T) {
	const path = "../../docs/schema.md"
	want := SchemaMarkdown()
	if os.Getenv("OPSDECK_UPDATE_DOCS") == "1" {
		if err := os.WriteFile(path, []byte(want), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != want {
		t.Fatal("docs/schema.md is out of date with kinds.go: run OPSDECK_UPDATE_DOCS=1 go test ./internal/workspace")
	}
}
