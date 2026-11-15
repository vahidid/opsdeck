package server

import (
	"fmt"
	"testing"

	"opsdeck/internal/testutil/fakellm"
)

// The chat gets the session's tokens and context during a turn, the
// window the provider reports, and the rate limits it sent.
func TestSessionContextUsageAndProviderLimits(t *testing.T) {
	h := newHarness(t)
	c := h.c
	fake := h.model([]fakellm.Turn{
		{Text: "Looking.", Tools: []fakellm.ToolCall{{Name: "inventory", Args: map[string]any{}}}},
		{Text: "Done."},
	})
	fake.ContextLength = 128000
	fake.Headers = map[string]string{"x-ratelimit-limit-requests": "60", "x-ratelimit-remaining-requests": "59"}

	// before any response, nothing is reported
	var limits []map[string]any
	c.must("GET", "/api/providers/limits", nil, &limits)
	if len(limits) != 1 || limits[0]["id"] != "fake" {
		t.Fatalf("limits: %v", limits)
	}

	sid, st := h.session(map[string]any{"mode": "ask"})
	c.must("POST", "/api/workspaces/acme/sessions/"+sid+"/messages", map[string]string{"text": "hi"}, nil)
	// a usage event after the first response, before the turn ends
	ev := st.wait(isType("usage"), "usage event")
	usage := ev["usage"].(map[string]any)
	if usage["inputTokens"] != 100.0 || usage["outputTokens"] != 20.0 {
		t.Fatalf("usage event: %v", ev)
	}
	st.wait(idle, "idle")

	var got struct {
		Session struct {
			Usage   map[string]float64 `json:"usage"`
			Context map[string]any     `json:"context"`
		} `json:"session"`
	}
	c.must("GET", "/api/workspaces/acme/sessions/"+sid, nil, &got)
	ctx := got.Session.Context
	if got.Session.Usage["inputTokens"] != 200 || got.Session.Usage["outputTokens"] != 40 {
		t.Fatalf("usage: %v", got.Session.Usage)
	}
	if ctx["model"] != "fake/m1" || ctx["used"] != 120.0 {
		t.Fatalf("context: %v", ctx)
	}
	// the fake lists model "scripted", not "m1": its window is not reported
	if _, ok := ctx["window"]; ok {
		t.Fatalf("a window the provider did not report: %v", ctx)
	}

	// the snapshot says how recent it is: not older than any event the
	// stream sent, so the chat can tell a stale reload from a fresh one
	var snap struct {
		Seq int `json:"seq"`
	}
	c.must("GET", "/api/workspaces/acme/sessions/"+sid, nil, &snap)
	maxID := 0
	st.mu.Lock()
	for _, ev := range st.all {
		var n int
		if id, _ := ev["id"].(string); len(id) > 1 {
			fmt.Sscanf(id[1:], "%d", &n)
		}
		if n > maxID {
			maxID = n
		}
	}
	st.mu.Unlock()
	if maxID == 0 || snap.Seq < maxID {
		t.Fatalf("seq %d is older than the stream's last event e%d", snap.Seq, maxID)
	}

	c.must("GET", "/api/providers/limits", nil, &limits)
	l := limits[0]["limits"].([]any)
	if len(l) != 1 || l[0].(map[string]any)["name"] != "requests" || l[0].(map[string]any)["remaining"] != 59.0 || limits[0]["observed"] == nil {
		t.Fatalf("limits: %v", limits)
	}
}
