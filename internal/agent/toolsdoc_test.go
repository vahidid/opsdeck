package agent

import (
	"os"
	"testing"
)

func TestToolReferenceIsCurrent(t *testing.T) {
	const path = "../../plugins/opsdeck/skills/opsdeck/references/tools.md"
	want := ToolReference()
	if os.Getenv("OPSDECK_UPDATE_DOCS") == "1" {
		os.MkdirAll("../../plugins/opsdeck/skills/opsdeck/references", 0o755)
		if err := os.WriteFile(path, []byte(want), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != want {
		t.Fatal("the skill's references/tools.md is out of date: run OPSDECK_UPDATE_DOCS=1 go test ./internal/agent -run TestToolReference")
	}
}
