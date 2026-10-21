package mcpx

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"opsdeck/internal/vault"
	"opsdeck/internal/workspace"
)

// The test binary doubles as a stdio MCP server when OPSDECK_TEST_MCP=1.
func TestMain(m *testing.M) {
	if os.Getenv("OPSDECK_TEST_MCP") == "1" {
		srv := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "1"}, nil)
		type in struct {
			Text string `json:"text"`
		}
		mcp.AddTool(srv, &mcp.Tool{Name: "echo", Description: "Echo text", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}},
			func(ctx context.Context, req *mcp.CallToolRequest, args in) (*mcp.CallToolResult, any, error) {
				return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "echo:" + args.Text + ":" + os.Getenv("SECRET")}}}, nil, nil
			})
		srv.Run(context.Background(), &mcp.StdioTransport{})
		return
	}
	os.Exit(m.Run())
}

func TestMCPServer(t *testing.T) {
	v, err := vault.Open(filepath.Join(t.TempDir(), "v.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer v.Close()
	if err := v.Init("password123"); err != nil {
		t.Fatal(err)
	}
	if _, err := v.Create(vault.Item{Scope: vault.GlobalScope, Type: "token", Name: "tok", Fields: []vault.Field{{Name: "token", Value: "tok-value"}}}); err != nil {
		t.Fatal(err)
	}
	cfg := workspace.MCPServer{Name: "tools", Command: os.Args[0], Env: map[string]string{"OPSDECK_TEST_MCP": "1", "SECRET": "vault://global/tok"}}
	w := &workspace.Workspace{Metadata: workspace.Meta{Name: "acme"}, MCPServers: []workspace.MCPServer{cfg}}

	m := New(v)
	trusted := false
	m.Trusted = func(ws string, c workspace.MCPServer) bool { return trusted }
	defer m.CloseAll()
	ctx := context.Background()

	m.Ensure(ctx, w)
	if st := m.Statuses(w); st[0].Status != "error" || !strings.Contains(st[0].Error, "not trusted") {
		t.Fatalf("untrusted server must not start: %+v", st)
	}
	trusted = true
	m.Ensure(ctx, w)
	st := m.Statuses(w)
	if st[0].Status != "running" || len(st[0].Tools) != 1 || !st[0].Tools[0].ReadOnly {
		t.Fatalf("status: %+v", st)
	}
	tools := m.Tools(w)
	if tools[0].FullName != "mcp__tools__echo" {
		t.Fatalf("name: %s", tools[0].FullName)
	}
	out, isErr, err := m.Call(ctx, w, "tools", "echo", []byte(`{"text":"hi"}`))
	if err != nil || isErr || strings.TrimSpace(out) != "echo:hi:tok-value" {
		t.Fatalf("call: %q %v %v", out, isErr, err)
	}
}
