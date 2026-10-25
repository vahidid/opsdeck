package mcpx

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"

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
	ctx := context.Background()
	httpClient := &http.Client{Transport: bearer{token: token, base: http.DefaultTransport}}
	client := mcp.NewClient(&mcp.Implementation{Name: "opsdeck-stdio", Version: version}, nil)
	upstream, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: url, HTTPClient: httpClient}, nil)
	if err != nil {
		return fmt.Errorf("connect to opsdeck: %w", err)
	}
	defer upstream.Close()
	tools, err := upstream.ListTools(ctx, nil)
	if err != nil {
		return err
	}
	srv := mcp.NewServer(&mcp.Implementation{Name: "opsdeck", Version: version}, nil)
	for _, t := range tools.Tools {
		name := t.Name
		srv.AddTool(t, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return upstream.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: req.Params.Arguments})
		})
	}
	return srv.Run(ctx, &mcp.StdioTransport{})
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
