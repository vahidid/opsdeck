package agent

import "testing"

func TestDockerActionsHaveRisk(t *testing.T) {
	acts := dockerActions()
	if len(acts) != len(dockerRisk) {
		t.Fatalf("%d actions in the schema, %d with a risk class", len(acts), len(dockerRisk))
	}
	for _, a := range acts {
		if _, ok := dockerRisk[a]; !ok {
			t.Errorf("action %s has no risk class", a)
		}
	}
	for _, a := range []string{"rm", "compose_down", "compose_down_volumes", "volume_delete", "volume_rm", "stack_rm", "service_rm", "prune", "image_rm"} {
		if dockerRisk[a] != RiskDestructive {
			t.Errorf("%s must be destructive", a)
		}
	}
	for _, a := range []string{"ps", "logs", "compose_ls", "compose_file", "services", "volume_ls", "volume_read"} {
		if dockerRisk[a] != RiskRead {
			t.Errorf("%s must be a read", a)
		}
	}
}
