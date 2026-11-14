package mcpx

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// RelayStdio relays the opsdeck MCP bridge (streamable HTTP) over stdio, for
// agents that can only launch stdio MCP servers. The session token is read
// from OPSDECK_MCP_TOKEN so it never appears in process listings.
func RelayStdio(url, version string) error {
	token := os.Getenv("OPSDECK_MCP_TOKEN")
	if url == "" || token == "" {
		return fmt.Errorf("usage: OPSDECK_MCP_TOKEN=<token> opsdeck mcp-stdio --url http://127.0.0.1:7788/mcp/")
	}
	return relay(url, token, version)
}

// ServeAccessKey is `opsdeck mcp`: the MCP server for agents outside opsdeck
// over stdio, relayed to the running opsdeck. The access key comes from
// OPSDECK_TOKEN (so it stays out of process listings and shell history).
func ServeAccessKey(url, version string) error {
	token := os.Getenv("OPSDECK_TOKEN")
	if token == "" {
		return fmt.Errorf("set OPSDECK_TOKEN to an access key (make one in opsdeck under App settings › Agent access)")
	}
	if err := relay(url, token, version); err != nil {
		if strings.Contains(err.Error(), "connection refused") {
			return fmt.Errorf("opsdeck is not running at %s: start the opsdeck app (or `opsdeck serve`) first", url)
		}
		return err
	}
	return nil
}

func relay(url, token, version string) error {
	return relayOver(context.Background(), url, token, version, &mcp.StdioTransport{})
}

// RelayCallKey marks a call relayed by `opsdeck mcp` (in _meta), so that
// opsdeck's approval for it (elicitation, in its _meta too) finds the
// agent's call it belongs to.
const RelayCallKey = "io.opsdeck/relay-call"

// relayOver relays opsdeck's MCP server to the agent on transport. An
// approval opsdeck asks (elicitation) is passed on to the agent when it can
// show a form, in the way its protocol version wants; otherwise the relay
// cannot answer and the approval waits in the opsdeck app.
func relayOver(ctx context.Context, url, token, version string, transport mcp.Transport) error {
	var calls Calls
	var mu sync.Mutex
	asks := map[string]Ask{} // the relayed calls that can ask their agent
	httpClient := &http.Client{Transport: bearer{token: token, base: http.DefaultTransport}}
	client := mcp.NewClient(&mcp.Implementation{Name: "opsdeck-stdio", Version: version}, &mcp.ClientOptions{
		ElicitationHandler: func(ctx context.Context, req *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
			tok, _ := req.Params.Meta[RelayCallKey].(string)
			mu.Lock()
			ask := asks[tok]
			mu.Unlock()
			if ask == nil {
				return nil, errors.New("the agent cannot show this request")
			}
			p := *req.Params
			p.Meta = nil
			return ask(ctx, &p)
		},
	})
	upstream, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: url, HTTPClient: httpClient}, nil)
	if err != nil {
		return fmt.Errorf("connect to opsdeck: %w", err)
	}
	defer upstream.Close()
	tools, err := upstream.ListTools(ctx, nil)
	if err != nil {
		return err
	}
	srv := mcp.NewServer(&mcp.Implementation{Name: "opsdeck", Version: version}, &mcp.ServerOptions{Instructions: upstream.InitializeResult().Instructions})
	for _, t := range tools.Tools {
		name := t.Name
		srv.AddTool(t, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			args := req.Params.Arguments
			return calls.Run(ctx, "", req, func(ctx context.Context, ask Ask) *mcp.CallToolResult {
				tok := uuid.NewString()
				if ask != nil {
					mu.Lock()
					asks[tok] = ask
					mu.Unlock()
					defer func() {
						mu.Lock()
						delete(asks, tok)
						mu.Unlock()
					}()
				}
				res, err := upstream.CallTool(ctx, &mcp.CallToolParams{Meta: mcp.Meta{RelayCallKey: tok}, Name: name, Arguments: args})
				if err != nil {
					return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: err.Error()}}}
				}
				return res
			})
		})
	}
	ss, err := srv.Connect(ctx, transport, nil)
	if err != nil {
		return err
	}
	return ss.Wait()
}

type bearer struct {
	token string
	base  http.RoundTripper
}

func (b bearer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.token)
	return b.base.RoundTrip(r)
}
