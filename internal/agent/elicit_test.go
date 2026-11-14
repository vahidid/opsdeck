package agent

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// A card that asks for a password is never sent to the connected client;
// any other approval is, with the facts of the card.
func TestElicitationSkipsPasswordCards(t *testing.T) {
	r, s := testRunner(t, ModeAsk)
	var asked atomic.Int32
	var msg atomic.Value
	ctx := withElicitor(context.Background(), func(ctx context.Context, m string) (string, string, bool) {
		asked.Add(1)
		msg.Store(m)
		return "approve", "", true
	})

	// with a password: only the app can answer
	done := make(chan approval, 1)
	go func() {
		done <- r.waitApproval(ctx, map[string]any{"callId": "c1", "tool": "ansible_run", "secret": map[string]any{"label": "vault"}}, func(string) error { return nil })
	}()
	var id string
	for i := 0; i < 200 && id == ""; i++ {
		s.mu.Lock()
		for k := range s.pending {
			id = k
		}
		s.mu.Unlock()
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(50 * time.Millisecond)
	if asked.Load() != 0 {
		t.Fatal("a password card was sent to the client")
	}
	if err := s.Resolve(id, "deny", "", "", false); err != nil {
		t.Fatal(err)
	}
	if a := <-done; a.decision != "deny" || a.by != "" {
		t.Fatalf("answer: %+v", a)
	}

	// without: the client answers
	a := r.waitApproval(ctx, map[string]any{"callId": "c2", "tool": "write_file", "target": "host/app-1", "environment": "prod", "risk": "change",
		"reason": "protected environment", "preview": "echo hi", "hosts": []string{"app-1", "app-2"}, "canary": true}, nil)
	if a.decision != "approve" || !a.canary || a.by == "" {
		t.Fatalf("answer: %+v", a)
	}
	m, _ := msg.Load().(string)
	for _, want := range []string{"write_file on host/app-1 in prod (change)", "protected environment", "app-1 first", "echo hi"} {
		if !strings.Contains(m, want) {
			t.Errorf("message lacks %q: %q", want, m)
		}
	}
}
