package agent

import "testing"

func TestKubeActionsHaveRisk(t *testing.T) {
	acts := kubeActions()
	if len(acts) != len(kubeRisk) {
		t.Fatalf("%d actions in the schema, %d with a risk class", len(acts), len(kubeRisk))
	}
	for _, a := range acts {
		if _, ok := kubeRisk[a]; !ok {
			t.Errorf("action %s has no risk class", a)
		}
	}
	for _, a := range []string{"delete", "delete_pod", "drain"} {
		if kubeRisk[a] != RiskDestructive {
			t.Errorf("%s must be destructive", a)
		}
	}
	for _, a := range []string{"get", "describe", "yaml", "logs", "events", "top", "overview", "history", "helm"} {
		if kubeRisk[a] != RiskRead {
			t.Errorf("%s must be a read", a)
		}
	}
}
