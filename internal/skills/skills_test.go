package skills

import (
	"strings"
	"testing"
)

func TestLibrary(t *testing.T) {
	lib := Library()
	if len(lib) != 20 {
		t.Fatalf("want 20 built-in skills, got %d", len(lib))
	}
	for _, s := range lib {
		if s.Description == "" || s.Category == "" {
			t.Errorf("%s: description %q category %q", s.Name, s.Description, s.Category)
		}
		body, err := Read(s.Name, "")
		if err != nil || !strings.HasPrefix(body, "---") {
			t.Errorf("%s: %v", s.Name, err)
		}
		for _, f := range s.Files {
			if _, err := Read(s.Name, f); err != nil {
				t.Errorf("%s/%s: %v", s.Name, f, err)
			}
		}
	}
	if _, err := Read("connectivity-triage", "../helm-chart-scaffolding/SKILL.md"); err == nil {
		t.Error("escape allowed")
	}
	if !strings.Contains(Attribution(), "Seth Hobson") {
		t.Error("attribution missing")
	}
}
