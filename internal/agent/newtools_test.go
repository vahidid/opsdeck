package agent

import (
	"testing"
)

func TestNewToolsRegistered(t *testing.T) {
	tools := Tools()
	found := map[string]bool{}
	for _, tool := range tools {
		found[tool.Def.Name] = true
	}

	required := []string{"terraform", "hcloud", "helm", "security_scan"}
	for _, req := range required {
		if !found[req] {
			t.Errorf("tool %q not registered in Tools()", req)
		}
	}
}

func TestTerraformAnalysis(t *testing.T) {
	planOut := `
Terraform will perform the following actions:

Plan: 2 to add, 1 to change, 1 to destroy.
`
	res := analyzeTerraformPlan(planOut)
	if !contains(res, "Blast Radius Summary") {
		t.Errorf("expected summary in plan analysis")
	}
	if !contains(res, "WARNING: This plan will DESTROY existing infrastructure") {
		t.Errorf("expected destroy warning in plan analysis")
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(substr) == 0 || (len(s) > 0 && len(substr) > 0 && containsSubstr(s, substr)))
}

func containsSubstr(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
