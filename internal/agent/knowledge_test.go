package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"opsdeck/internal/llm"
	"opsdeck/internal/workspace"
)

func TestRecall(t *testing.T) {
	dir := t.TempDir()
	write := func(rel, s string) {
		p := filepath.Join(dir, rel)
		os.MkdirAll(filepath.Dir(p), 0o700)
		if err := os.WriteFile(p, []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("workspace.yaml", "kind: Workspace\nmetadata:\n  name: acme\n")
	write("skills/deploy-api.md", "---\nname: deploy-api\ndescription: Deploy the api service to the docker hosts with zero downtime\n---\n# Deploy api\n1. pull the image\n2. roll hosts one by one\n")
	write("docs/runbooks/postgres-restore.md", "# Restore Postgres\nRestore the nightly pg_dump backup from the NAS with pg_restore.\n")
	write("docs/runbooks/deploy-api.md", "# Deploying the api\nThe api runs as a docker container on web-1 and web-2 behind haproxy. Deploy it with the deploy-api skill.\n")
	write("reports/2026-10-01-disk.md", "# Disk full on web-2\n/var/log filled up; logrotate was missing.\n")
	w, err := workspace.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	env := &Env{WS: w, MemoryPath: filepath.Join(dir, ".opsdeck", "memory.md")}
	os.MkdirAll(filepath.Dir(env.MemoryPath), 0o700)
	os.WriteFile(env.MemoryPath, []byte("- api hosts are docker-1 and docker-2\n"), 0o600)

	r, block := recall(env, "deploy the new api version", nil)
	if r == nil || len(r.Items) != 2 || r.Preloaded != "deploy-api" || !strings.Contains(block, "roll hosts one by one") {
		t.Fatalf("recall: %+v\n%s", r, block)
	}
	// already loaded in this conversation: listed, not included again
	input, _ := json.Marshal(map[string]string{"name": "deploy-api"})
	hist := []llm.Message{{Role: "assistant", Content: []llm.Block{{Type: "tool_use", Name: "load_skill", Input: input}}}}
	if r, block := recall(env, "deploy the api again", hist); r == nil || r.Preloaded != "" || strings.Contains(block, "<skill name=") {
		t.Fatalf("loaded twice: %+v", r)
	}
	if r, _ := recall(env, "بکاپ پایگاه داده رو بازیابی کن", nil); r == nil || r.Items[0].Ref != "docs/runbooks/postgres-restore.md" {
		t.Fatalf("persian: %+v", r)
	}
	if r, _ := recall(env, "web-2 disk is full again", nil); r == nil || r.Items[0].Kind != "report" {
		t.Fatalf("report: %+v", r)
	}
	// the built-in library
	for q, want := range map[string]string{
		"write a postmortem for yesterday's outage":   "postmortem-writing",
		"set up prometheus alerting rules for the db": "prometheus-configuration",
		"create a helm chart for the api":             "helm-chart-scaffolding",
	} {
		r, _ := recall(env, q, nil)
		found := false
		for i := 0; r != nil && i < len(r.Items) && i < 2; i++ {
			found = found || r.Items[i].Ref == want
		}
		if !found {
			t.Errorf("%q: %+v", q, r)
		}
	}
	for _, q := range []string{"ok", "thanks!", "بله ادامه بده", "yes do it"} {
		if r, _ := recall(env, q, nil); r != nil {
			t.Errorf("%q matched: %+v", q, r)
		}
	}
	// memory is found by the tool, and the index follows changes
	os.WriteFile(env.MemoryPath, []byte("- api hosts are docker-1 and docker-2\n- the NAS is only reachable over office-vpn\n"), 0o600)
	if h := knowledgeIndex(env).Search("NAS vpn", "memory", 1); len(h) != 1 || !strings.Contains(h[0].Text, "office-vpn") {
		t.Fatalf("memory: %+v", h)
	}
}
