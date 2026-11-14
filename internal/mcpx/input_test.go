package mcpx

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// pausingServer has a tool "change" that asks one question through Calls;
// its owner is the "owner" argument (an access key's session in opsdeck).
// The client does its own retries, so the test can replay states.
func pausingServer(t *testing.T) *mcp.ClientSession {
	t.Helper()
	var calls Calls
	srv := mcp.NewServer(&mcp.Implementation{Name: "s", Version: "1"}, nil)
	srv.AddTool(&mcp.Tool{Name: "change", InputSchema: map[string]any{"type": "object"}}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var args struct{ Owner string }
		if len(req.Params.Arguments) > 0 {
			_ = jsonUnmarshal(req.Params.Arguments, &args)
		}
		return calls.Run(ctx, args.Owner, req, func(ctx context.Context, ask Ask) *mcp.CallToolResult {
			res, err := ask(ctx, &mcp.ElicitParams{Message: "approve?", RequestedSchema: map[string]any{"type": "object", "properties": map[string]any{"decision": map[string]any{"type": "string"}}}})
			if err != nil {
				return errorResult("no answer: " + err.Error())
			}
			d, _ := res.Content["decision"].(string)
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "ran for " + args.Owner + ": " + d}}}
		})
	})
	ct, st := mcp.NewInMemoryTransports()
	ctx := context.Background()
	if _, err := srv.Connect(ctx, st, nil); err != nil {
		t.Fatal(err)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "c", Version: "1"}, &mcp.ClientOptions{
		MultiRoundTrip: &mcp.MultiRoundTripOptions{Disabled: true},
		ElicitationHandler: func(context.Context, *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
			return nil, nil // never used: the test answers through retries
		},
	})
	sess, err := client.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sess.Close() })
	return sess
}

func text(r *mcp.CallToolResult) string {
	var b strings.Builder
	for _, c := range r.Content {
		if t, ok := c.(*mcp.TextContent); ok {
			b.WriteString(t.Text)
		}
	}
	return b.String()
}

func approve(state, owner string) *mcp.CallToolParams {
	return &mcp.CallToolParams{Name: "change", Arguments: map[string]any{"owner": owner}, RequestState: state,
		InputResponses: mcp.InputResponseMap{InputKey: &mcp.ElicitResult{Action: "accept", Content: map[string]any{"decision": "approve"}}}}
}

// Another owner cannot answer a paused call or get its result; the call
// stays open for its own client.
func TestPausedCallBelongsToItsOwner(t *testing.T) {
	sess := pausingServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	first, err := sess.CallTool(ctx, &mcp.CallToolParams{Name: "change", Arguments: map[string]any{"owner": "acme/mcp-a"}})
	if err != nil || !first.NeedsInput() || first.RequestState == "" {
		t.Fatalf("first: %v %+v", err, first)
	}
	other, err := sess.CallTool(ctx, approve(first.RequestState, "other/mcp-b"))
	if err != nil || !other.IsError || !strings.Contains(text(other), "no longer open") {
		t.Fatalf("another owner: %v %+v %q", err, other, text(other))
	}
	own, err := sess.CallTool(ctx, approve(first.RequestState, "acme/mcp-a"))
	if err != nil || own.IsError || text(own) != "ran for acme/mcp-a: approve" {
		t.Fatalf("own: %v %q", err, text(own))
	}
}

// Two retries with the same state: one is served, the other is told the
// request is already being answered; the call runs once.
func TestPausedCallServesOneRetryAtATime(t *testing.T) {
	sess := pausingServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	first, err := sess.CallTool(ctx, &mcp.CallToolParams{Name: "change", Arguments: map[string]any{"owner": "o"}})
	if err != nil || !first.NeedsInput() {
		t.Fatalf("first: %v %+v", err, first)
	}
	var wg sync.WaitGroup
	results := make([]string, 4)
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			r, err := sess.CallTool(ctx, approve(first.RequestState, "o"))
			if err != nil {
				results[i] = "error: " + err.Error()
				return
			}
			results[i] = text(r)
		}(i)
	}
	wg.Wait()
	ran, busy := 0, 0
	for _, r := range results {
		switch {
		case r == "ran for o: approve":
			ran++
		case strings.Contains(r, "already being answered"), strings.Contains(r, "no longer open"):
			busy++
		default:
			t.Errorf("unexpected: %q", r)
		}
	}
	if ran != 1 || busy != len(results)-1 {
		t.Fatalf("ran %d, refused %d: %q", ran, busy, results)
	}
}

func jsonUnmarshal(b []byte, v any) error { return json.Unmarshal(b, v) }
