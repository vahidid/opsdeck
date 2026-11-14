package mcpx

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// opsdeck's approval request (elicitation) passes through `opsdeck mcp` to
// the agent behind it, and the answer comes back.
func TestRelayPassesElicitation(t *testing.T) {
	// like opsdeck: asks through Calls and returns the relay's call marker
	var calls Calls
	upstream := mcp.NewServer(&mcp.Implementation{Name: "up", Version: "1"}, nil)
	upstream.AddTool(&mcp.Tool{Name: "change", InputSchema: map[string]any{"type": "object"}}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		meta := mcp.Meta{RelayCallKey: req.Params.Meta[RelayCallKey]}
		return calls.Run(ctx, req, func(ctx context.Context, ask Ask) *mcp.CallToolResult {
			text := func(s string) *mcp.CallToolResult {
				return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: s}}}
			}
			if ask == nil {
				return text("no form")
			}
			res, err := ask(ctx, &mcp.ElicitParams{Meta: meta, Message: "approve?", RequestedSchema: map[string]any{"type": "object", "properties": map[string]any{"decision": map[string]any{"type": "string"}}}})
			if err != nil {
				return text("no form: " + err.Error())
			}
			d, _ := res.Content["decision"].(string)
			return text(res.Action + ":" + d)
		})
	})
	ts := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return upstream }, nil))
	defer ts.Close()

	for _, tc := range []struct {
		name string
		opts *mcp.ClientOptions
		want string
	}{
		{"form", &mcp.ClientOptions{ElicitationHandler: func(ctx context.Context, req *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
			return &mcp.ElicitResult{Action: "accept", Content: map[string]any{"decision": "approve"}}, nil
		}}, "accept:approve"},
		{"no form", nil, "no form"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			agentSide, relaySide := mcp.NewInMemoryTransports()
			go relayOver(ctx, ts.URL, "odk_test", "1", relaySide)
			sess, err := mcp.NewClient(&mcp.Implementation{Name: "agent", Version: "1"}, tc.opts).Connect(ctx, agentSide, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer sess.Close()
			res, err := sess.CallTool(ctx, &mcp.CallToolParams{Name: "change", Arguments: map[string]any{}})
			if err != nil {
				t.Fatal(err)
			}
			got := res.Content[0].(*mcp.TextContent).Text
			if len(got) < len(tc.want) || got[:len(tc.want)] != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}
