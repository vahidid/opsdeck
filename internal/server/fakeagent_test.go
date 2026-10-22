package server

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"opsdeck/internal/gitx"
)

// TestMain lets the test binary act as a fake Claude Code CLI
// (OPSDECK_FAKE_CLAUDE=1): it reads the MCP config opsdeck passes, calls
// opsdeck tools through the bridge and prints stream-json like `claude -p`.
//
// It also acts as `opsdeck socks-connect` (ssh's ProxyCommand through a VPN
// or jump host, used by git and ansible).
func TestMain(m *testing.M) {
	if os.Getenv("OPSDECK_FAKE_CLAUDE") == "1" {
		os.Exit(fakeClaude())
	}
	if len(os.Args) > 1 && os.Args[1] == "socks-connect" {
		if err := gitx.SocksConnect(os.Args[2:], os.Stdin, os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func fakeClaude() int {
	args := os.Args[1:]
	flag := func(name string) string {
		for i, a := range args {
			if a == name && i+1 < len(args) {
				return args[i+1]
			}
		}
		return ""
	}
	for _, must := range []string{"--strict-mcp-config", "--tools", "--allowedTools"} {
		if flag(must) == "" && must != "--strict-mcp-config" && must != "--tools" {
			fmt.Fprintln(os.Stderr, "missing", must)
			return 2
		}
	}
	if flag("--tools") != "" {
		fmt.Fprintln(os.Stderr, "built-in tools must be disabled")
		return 2
	}
	session := flag("--session-id")
	if r := flag("--resume"); r != "" {
		session = r
	}
	prompt, _ := io.ReadAll(os.Stdin)
	var cfg struct {
		MCPServers map[string]struct {
			URL     string            `json:"url"`
			Headers map[string]string `json:"headers"`
		} `json:"mcpServers"`
	}
	b, err := os.ReadFile(flag("--mcp-config"))
	if err != nil || json.Unmarshal(b, &cfg) != nil {
		fmt.Fprintln(os.Stderr, "bad mcp config", err)
		return 2
	}
	srv := cfg.MCPServers["opsdeck"]
	out := json.NewEncoder(os.Stdout)
	out.Encode(map[string]any{"type": "system", "subtype": "init", "session_id": session,
		"mcp_servers": []map[string]string{{"name": "opsdeck", "status": "connected"}}})

	hc := &http.Client{Transport: headerRT{srv.Headers}}
	ctx := context.Background()
	cl := mcp.NewClient(&mcp.Implementation{Name: "fake-claude", Version: "0"}, nil)
	cs, err := cl.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: srv.URL, HTTPClient: hc}, nil)
	if err != nil {
		fmt.Fprintln(os.Stderr, "connect:", err)
		return 1
	}
	defer cs.Close()
	var said []string
	// The prompt lists commands to run, one per line, as "run <host> <command>".
	sc := bufio.NewScanner(strings.NewReader(string(prompt)))
	for sc.Scan() {
		host, cmd, ok := strings.Cut(strings.TrimPrefix(sc.Text(), "run "), " ")
		if !ok || !strings.HasPrefix(sc.Text(), "run ") {
			continue
		}
		res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "exec", Arguments: map[string]any{"target": host, "command": cmd}})
		if err != nil {
			said = append(said, "error: "+err.Error())
			continue
		}
		text := ""
		for _, c := range res.Content {
			if t, ok := c.(*mcp.TextContent); ok {
				text += t.Text
			}
		}
		said = append(said, fmt.Sprintf("%s → error=%v %s", cmd, res.IsError, strings.TrimSpace(text)))
	}
	reply := "Results:\n" + strings.Join(said, "\n")
	out.Encode(map[string]any{"type": "stream_event", "session_id": session, "event": map[string]any{"type": "message_start", "message": map[string]any{"id": "msg_1"}}})
	out.Encode(map[string]any{"type": "stream_event", "session_id": session, "event": map[string]any{"type": "content_block_delta", "delta": map[string]any{"type": "text_delta", "text": reply}}})
	out.Encode(map[string]any{"type": "assistant", "session_id": session, "message": map[string]any{"id": "msg_1", "content": []map[string]any{{"type": "text", "text": reply}}}})
	out.Encode(map[string]any{"type": "result", "subtype": "success", "is_error": false, "session_id": session, "usage": map[string]int{"input_tokens": 10, "output_tokens": 5}})
	return 0
}

type headerRT struct{ h map[string]string }

func (h headerRT) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	for k, v := range h.h {
		r.Header.Set(k, v)
	}
	return http.DefaultTransport.RoundTrip(r)
}
