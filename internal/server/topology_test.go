package server

import (
	"bufio"
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"opsdeck/internal/paths"
	"opsdeck/internal/testutil/fakellm"
)

// TestTopologyAndSkills: the agent sees the topology and the built-in skill
// library in its prompt, loads a skill with a supporting file, and changes
// the topology only after the user approves (once, then "always").
func TestTopologyAndSkills(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("OPSDECK_HOME", t.TempDir())
	srv, err := New(paths.Default(), Options{Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	jar, _ := cookiejar.New(nil)
	c := &client{t: t, base: ts.URL, http: &http.Client{Jar: jar, Timeout: 60 * time.Second}}
	c.must("POST", "/api/auth/login", map[string]string{"token": srv.token}, nil)
	c.must("POST", "/api/vault/init", map[string]string{"password": "correct horse battery"}, nil)
	c.must("POST", "/api/workspaces", map[string]string{"name": "acme"}, nil)
	c.must("POST", "/api/workspaces/acme/resources", map[string]any{"kind": "Host", "name": "app-1", "environment": "prod", "spec": map[string]any{"address": "10.0.0.5"}}, nil)

	// The user draws a first version.
	var saved map[string]any
	c.must("PUT", "/api/workspaces/acme/diagram", map[string]any{"rev": "", "topology": map[string]any{
		"zones": []any{map[string]any{"id": "dc", "label": "DC", "kind": "datacenter"}},
		"nodes": []any{map[string]any{"id": "lb", "type": "load-balancer", "label": "HAProxy", "zone": "dc"}},
	}}, &saved)
	var cat map[string]any
	c.must("GET", "/api/topology/catalog", nil, &cat)
	if len(cat["nodeTypes"].([]any)) < 40 {
		t.Fatalf("catalog: %d node types", len(cat["nodeTypes"].([]any)))
	}
	var lib map[string]any
	c.must("GET", "/api/skills/library", nil, &lib)
	if len(lib["skills"].([]any)) != 20 || !strings.Contains(lib["attribution"].(string), "MIT") {
		t.Fatalf("library: %v", len(lib["skills"].([]any)))
	}

	fake := fakellm.New([]fakellm.Turn{
		{Tools: []fakellm.ToolCall{{Name: "load_skill", Args: map[string]any{"name": "connectivity-triage", "file": "references/details.md"}}}},
		{Tools: []fakellm.ToolCall{{Name: "topology", Args: map[string]any{"action": "batch", "operations": []any{
			map[string]any{"action": "add_node", "node": map[string]any{"id": "cf", "type": "cdn", "label": "Cloudflare", "vendor": "Cloudflare", "tech": []string{"WAF"}}},
			map[string]any{"action": "import_inventory"},
			map[string]any{"action": "connect", "link": map[string]any{"from": "cf", "to": "lb", "kind": "https", "port": "443", "encrypted": true}},
			map[string]any{"action": "update_node", "id": "app-1", "node": map[string]any{"zone": "dc", "deployments": []any{map[string]any{"name": "shop", "kind": "docker-compose", "ports": []string{"8080"}}}}},
		}}}}},
		{Tools: []fakellm.ToolCall{{Name: "topology", Args: map[string]any{"action": "connect", "link": map[string]any{"from": "lb", "to": "app-1", "kind": "http", "port": "8080"}}}}},
		{Tools: []fakellm.ToolCall{{Name: "topology", Args: map[string]any{"action": "connect", "link": map[string]any{"from": "lb", "to": "nowhere"}}}}},
		{Tools: []fakellm.ToolCall{{Name: "topology", Args: map[string]any{"action": "get"}}}},
		{Text: "Done."},
	})
	defer fake.Close()
	var settings map[string]any
	c.must("GET", "/api/settings", nil, &settings)
	settings["providers"] = []map[string]any{{"id": "fake", "type": "openai", "name": "Fake", "baseUrl": fake.URL, "models": []string{"m1"}}}
	c.must("PUT", "/api/settings", settings, nil)
	var sess map[string]any
	c.must("POST", "/api/workspaces/acme/sessions", map[string]any{"mode": "auto"}, &sess)
	sid := sess["id"].(string)
	stream, err := c.http.Get(ts.URL + "/api/workspaces/acme/sessions/" + sid + "/stream")
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Body.Close()
	c.must("POST", "/api/workspaces/acme/sessions/"+sid+"/messages", map[string]string{"text": "put cloudflare in front"}, nil)
	var asked []string
	results := map[string]string{}
	started := false
	sc := bufio.NewScanner(stream.Body)
	sc.Buffer(make([]byte, 1<<20), 8<<20)
	for sc.Scan() {
		d, ok := strings.CutPrefix(sc.Text(), "data: ")
		if !ok {
			continue
		}
		var ev map[string]any
		json.Unmarshal([]byte(d), &ev)
		switch ev["type"] {
		case "approval_request":
			asked = append(asked, ev["tool"].(string)+": "+strings.SplitN(ev["preview"].(string), "\n", 2)[0])
			c.must("POST", "/api/workspaces/acme/sessions/"+sid+"/approvals/"+ev["approvalId"].(string), map[string]string{"decision": "always"}, nil)
		case "tool_result":
			out, _ := ev["output"].(string)
			if e, _ := ev["error"].(string); e != "" {
				out = "ERROR " + e
			}
			results[ev["callId"].(string)] = out
		case "status":
			if ev["state"] == "running" {
				started = true
			}
		}
		if started && ev["type"] == "status" && ev["state"] == "idle" {
			break
		}
	}
	// Even in Auto mode the first topology change asks; "always" covers the rest.
	if len(asked) != 1 || !strings.HasPrefix(asked[0], "topology: added node cf") {
		t.Fatalf("asked: %v", asked)
	}
	if !strings.Contains(results["call_0_0"], "Supporting files") && !strings.Contains(results["call_0_0"], "DNS") {
		t.Errorf("skill file: %.200s", results["call_0_0"])
	}
	if !strings.Contains(results["call_3_0"], "does not exist") {
		t.Errorf("invalid link accepted: %s", results["call_3_0"])
	}
	for _, want := range []string{"[cf] Cloudflare: CDN — Cloudflare; WAF", "cf → lb: HTTPS (port 443, encrypted)", "lb → app-1: HTTP (port 8080)",
		"runs shop (Docker Compose, ports 8080)", "inventory host/app-1"} {
		if !strings.Contains(results["call_4_0"], want) {
			t.Errorf("topology misses %q:\n%s", want, results["call_4_0"])
		}
	}
	sys := fake.LastSystem()
	for _, want := range []string{"## Topology (topology tool)", "[lb] HAProxy: Load balancer", "- connectivity-triage (Networking): ", "propose the change"} {
		if !strings.Contains(sys, want) {
			t.Errorf("system prompt misses %q", want)
		}
	}
	b, _ := os.ReadFile(filepath.Join(os.Getenv("OPSDECK_HOME"), "data", "workspaces", "acme", "topology.yaml"))
	if len(b) > 0 && !strings.Contains(string(b), "kind: https") {
		t.Errorf("file: %s", b)
	}
	// The UI reads it back; export for an AI.
	var got map[string]any
	c.must("GET", "/api/workspaces/acme/diagram", nil, &got)
	if n := len(got["topology"].(map[string]any)["nodes"].([]any)); n != 3 {
		t.Fatalf("nodes after agent: %d", n)
	}
	res, err := c.http.Get(ts.URL + "/api/workspaces/acme/diagram/export?format=md")
	if err != nil {
		t.Fatal(err)
	}
	md := make([]byte, 64<<10)
	n, _ := res.Body.Read(md)
	res.Body.Close()
	if !strings.Contains(string(md[:n]), "# Infrastructure topology: acme") || !strings.Contains(res.Header.Get("Content-Disposition"), "acme-topology.md") {
		t.Fatalf("export: %s", md[:n])
	}
	// Copy a built-in skill into the workspace: the copy wins.
	c.must("POST", "/api/workspaces/acme/skills/postmortem-writing/copy", nil, nil)
	var skills []map[string]any
	c.must("GET", "/api/workspaces/acme/skills", nil, &skills)
	for _, s := range skills {
		if s["name"] == "postmortem-writing" && s["builtin"] == true {
			t.Fatal("the workspace copy should replace the built-in skill")
		}
	}
}
