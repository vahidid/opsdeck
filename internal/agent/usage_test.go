package agent

import (
	"testing"

	"opsdeck/internal/llm"
)

func TestContextUsed(t *testing.T) {
	for _, tc := range []struct {
		u    llm.Usage
		want int
	}{
		{llm.Usage{}, 0},
		{llm.Usage{InputTokens: 1200, OutputTokens: 300}, 1500},
		// InputTokens already counts cache reads (Anthropic adds them in);
		// CacheRead is a part of it, not more.
		{llm.Usage{InputTokens: 1200, OutputTokens: 300, CacheRead: 1000}, 1500},
	} {
		if got := contextUsed(tc.u); got != tc.want {
			t.Errorf("contextUsed(%+v) = %d, want %d", tc.u, got, tc.want)
		}
	}
}

func TestSessionUsageAndContext(t *testing.T) {
	_, s := testRunner(t, ModeAsk)
	ch, unsubscribe := s.Subscribe()
	defer unsubscribe()
	next := func() Event {
		t.Helper()
		select {
		case ev := <-ch:
			return ev
		default:
			t.Fatal("no event")
		}
		return nil
	}

	// a subagent's tokens count in the totals, not in the session's context
	s.addUsage("p/m", llm.Usage{InputTokens: 500, OutputTokens: 50}, false)
	if ev := next(); ev["type"] != "usage" || ev["context"] != nil {
		t.Fatalf("event: %v", ev)
	}
	if s.info.Context != nil {
		t.Fatalf("context from a subagent: %+v", s.info.Context)
	}

	// the window arrives before the first response
	s.setWindow("p/m", 200000)
	if ev := next(); ev["type"] != "usage" || ev["context"].(ContextUse).Window != 200000 {
		t.Fatalf("event: %v", ev)
	}
	s.addUsage("p/m", llm.Usage{InputTokens: 1000, OutputTokens: 100}, true)
	ev := next()
	c := ev["context"].(ContextUse)
	if c.Used != 1100 || c.Window != 200000 || c.Model != "p/m" {
		t.Fatalf("context: %+v", c)
	}
	// the context is the latest request, the totals add up
	s.addUsage("p/m", llm.Usage{InputTokens: 1300, OutputTokens: 40}, true)
	next()
	if s.info.Context.Used != 1340 || s.info.Usage.InputTokens != 2800 || s.info.Usage.OutputTokens != 190 {
		t.Fatalf("context %+v usage %+v", s.info.Context, s.info.Usage)
	}
	// a response without usage keeps what was known
	s.addUsage("p/m", llm.Usage{}, true)
	next()
	if s.info.Context.Used != 1340 {
		t.Fatalf("context after a response without usage: %+v", s.info.Context)
	}
	// another model: its window is not the old one's
	s.addUsage("q/n", llm.Usage{InputTokens: 10, OutputTokens: 1}, true)
	next()
	if s.info.Context.Model != "q/n" || s.info.Context.Window != 0 || s.info.Context.Used != 11 {
		t.Fatalf("context after a model change: %+v", s.info.Context)
	}
	// the same window again sends nothing
	s.setWindow("q/n", 0)
	select {
	case ev := <-ch:
		t.Fatalf("unexpected event %v", ev)
	default:
	}
}
