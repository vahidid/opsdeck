package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"opsdeck/internal/config"
	"opsdeck/internal/llm"
	"opsdeck/internal/testutil/fakellm"
	"opsdeck/internal/workspace"
)

// subagentRunner is a runner in a Full-mode session whose subagents use the
// scripted model; write records that a change ran.
func subagentRunner(t *testing.T, fake *fakellm.Server, defs []workspace.AgentDef, wrote *[]string) (*runner, *Session) {
	t.Helper()
	r, s := testRunner(t, ModeFull,
		fakeTool("write", RiskChange, func(ctx context.Context) (string, error) {
			*wrote = append(*wrote, "write")
			return "written", nil
		}),
		toolDelegate(),
	)
	providers := []config.Provider{{ID: "fake", Type: "openai", BaseURL: fake.URL, Models: []string{"m1"}}}
	r.m.deps.Router = func() *llm.Router { return llm.NewRouter(providers, nil) }
	r.m.deps.Subagents = func() SubagentConfig { return SubagentConfig{Models: []string{"fake/m1"}} }
	s.info.Model = "fake/m1"
	r.env.Agents = defs
	return r, s
}

func delegateCall(id, agent string) llm.Block {
	in, _ := json.Marshal(map[string]any{"agent": agent, "task": "do the thing"})
	return llm.Block{Type: "tool_use", ID: id, Name: "delegate", Input: in}
}

// A workspace's own read-only agent runs in Plan mode like the built-in
// investigator, even in a Full-mode session; an agent that may change things
// runs in the session's mode.
func TestReadOnlySubagentsRunInPlanMode(t *testing.T) {
	writeThenReport := []fakellm.Turn{
		{Text: "Changing it.", Tools: []fakellm.ToolCall{{Name: "write", Args: map[string]any{}}}},
		{Text: "Report: done what I could."},
	}
	fake := fakellm.New(nil)
	fake.ByMarker = map[string][]fakellm.Turn{`subagent "auditor"`: writeThenReport, `subagent "fixer"`: writeThenReport}
	defer fake.Close()
	var wrote []string
	defs := []workspace.AgentDef{
		{Name: "auditor", ReadOnly: true, Description: "audits", Prompt: "Audit."},
		{Name: "fixer", ReadOnly: false, Description: "fixes", Prompt: "Fix."},
	}
	r, s := subagentRunner(t, fake, defs, &wrote)

	res := r.runCalls(context.Background(), []llm.Block{delegateCall("a1", "auditor")}, false)
	if res[0].IsError || len(wrote) != 0 {
		t.Fatalf("read-only subagent: result %q, writes %v", res[0].Content, wrote)
	}
	refused := false
	for _, ev := range s.Events() {
		if ev["parent"] == "a1" && ev["type"] == "tool_result" && strings.Contains(fmt.Sprint(ev["output"]), "plan mode is read-only") {
			refused = true
		}
		if ev["type"] == "subagent" && ev["callId"] == "a1" && ev["phase"] == "start" && ev["readOnly"] != true {
			t.Fatalf("start event: %v", ev)
		}
	}
	if !refused {
		t.Fatal("the read-only subagent's change was not refused by Plan mode")
	}

	res = r.runCalls(context.Background(), []llm.Block{delegateCall("f1", "fixer")}, false)
	if res[0].IsError || len(wrote) != 1 {
		t.Fatalf("subagent that may change: result %q, writes %v", res[0].Content, wrote)
	}
}

// The chat gets each subagent's tokens and context while it works and at
// its end; they count in the session's totals, not in its context.
func TestSubagentUsageAndContext(t *testing.T) {
	fake := fakellm.New(nil)
	fake.ByMarker = map[string][]fakellm.Turn{`subagent "investigator"`: {
		{Text: "Looking.", Tools: []fakellm.ToolCall{{Name: "write", Args: map[string]any{}}}},
		{Text: "Report: all fine."},
	}}
	fake.ContextLength = 64000
	defer fake.Close()
	var wrote []string
	r, s := subagentRunner(t, fake, builtinAgents, &wrote)
	r.m.deps.Router = func() *llm.Router {
		// the fake lists its model as "scripted"
		return llm.NewRouter([]config.Provider{{ID: "fake", Type: "openai", BaseURL: fake.URL, Models: []string{"scripted"}}}, nil)
	}
	r.m.deps.Subagents = func() SubagentConfig { return SubagentConfig{Models: []string{"fake/scripted"}} }
	s.info.Model = "fake/scripted"
	// the session asked for the window already (it is cached); the subagent
	// asks in the background and reports it with its numbers
	if n, err := r.m.deps.Router().ContextWindow(context.Background(), "fake/scripted"); err != nil || n != 64000 {
		t.Fatalf("window: %d %v", n, err)
	}
	ch, unsubscribe := s.Subscribe()
	defer unsubscribe()

	res := r.runCalls(context.Background(), []llm.Block{delegateCall("d1", "investigator")}, false)
	if res[0].IsError {
		t.Fatalf("result: %q", res[0].Content)
	}
	var live, end Event
	for len(ch) > 0 {
		ev := <-ch
		if ev["type"] != "subagent" {
			continue
		}
		switch ev["phase"] {
		case "usage":
			live = ev
		case "end":
			end = ev
		}
	}
	if live == nil || live["inputTokens"] != 100 || live["context"].(ContextUse).Used != 120 {
		t.Fatalf("live usage: %v", live)
	}
	c := end["context"].(ContextUse)
	if end["state"] != "done" || end["inputTokens"] != 200 || end["outputTokens"] != 40 || c.Used != 120 || c.Model != "fake/scripted" {
		t.Fatalf("end: %v", end)
	}
	if c.Window != 64000 {
		t.Fatalf("window: %d", c.Window)
	}
	if s.info.Usage.InputTokens != 200 || s.info.Context != nil {
		t.Fatalf("session usage %+v context %+v", s.info.Usage, s.info.Context)
	}
	// stored: start and end, not the live numbers
	for _, ev := range s.Events() {
		if ev["type"] == "subagent" && ev["phase"] == "usage" {
			t.Fatal("a live usage event was stored")
		}
	}
}
