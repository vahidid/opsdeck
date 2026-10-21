package extagent

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
)

// The test binary doubles as a fake ACP agent (OPSDECK_FAKE_ACP=1).
func TestMain(m *testing.M) {
	if os.Getenv("OPSDECK_FAKE_ACP") == "1" {
		fakeACPAgent()
		return
	}
	os.Exit(m.Run())
}

func fakeACPAgent() {
	in := bufio.NewScanner(os.Stdin)
	in.Buffer(make([]byte, 1<<20), 1<<24)
	out := json.NewEncoder(os.Stdout)
	nextID := 100
	var log []string
	reply := func(id json.RawMessage, result any) {
		out.Encode(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
	}
	// ask sends a request to the client and waits for its response.
	ask := func(method string, params any) json.RawMessage {
		nextID++
		out.Encode(map[string]any{"jsonrpc": "2.0", "id": nextID, "method": method, "params": params})
		for in.Scan() {
			var m map[string]json.RawMessage
			json.Unmarshal(in.Bytes(), &m)
			if string(m["id"]) == fmt.Sprint(nextID) {
				if m["error"] != nil {
					return m["error"]
				}
				return m["result"]
			}
		}
		return nil
	}
	update := func(sid string, u map[string]any) {
		out.Encode(map[string]any{"jsonrpc": "2.0", "method": "session/update", "params": map[string]any{"sessionId": sid, "update": u}})
	}
	for in.Scan() {
		var m struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		json.Unmarshal(in.Bytes(), &m)
		switch m.Method {
		case "initialize":
			reply(m.ID, map[string]any{"protocolVersion": 1, "agentCapabilities": map[string]any{"loadSession": true, "mcpCapabilities": map[string]bool{"http": true}}})
		case "session/new":
			log = append(log, "new:"+string(m.Params))
			reply(m.ID, map[string]string{"sessionId": "sess-1"})
		case "session/load":
			log = append(log, "load")
			update("sess-1", map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]string{"type": "text", "text": "OLD HISTORY"}})
			reply(m.ID, map[string]any{})
		case "session/prompt":
			var p struct {
				Prompt []struct{ Text string } `json:"prompt"`
			}
			json.Unmarshal(m.Params, &p)
			log = append(log, "prompt:"+p.Prompt[0].Text)
			update("sess-1", map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]string{"type": "text", "text": "Checking "}})
			opts := []map[string]string{{"optionId": "a", "kind": "allow_once"}, {"optionId": "r", "kind": "reject_once"}}
			r1 := ask("session/request_permission", map[string]any{"sessionId": "sess-1", "toolCall": map[string]any{"title": "exec (opsdeck MCP Server)"}, "options": opts})
			r2 := ask("session/request_permission", map[string]any{"sessionId": "sess-1", "toolCall": map[string]any{"title": "Shell rm -rf /", "kind": "execute"}, "options": opts})
			r3 := ask("fs/read_text_file", map[string]any{"path": "/etc/passwd"})
			update("sess-1", map[string]any{"sessionUpdate": "tool_call", "toolCallId": "t1", "title": "exec"})
			update("sess-1", map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]string{"type": "text", "text": fmt.Sprintf("perm1=%s perm2=%s fs=%s log=%s", r1, r2, r3, strings.Join(log, "|"))}})
			reply(m.ID, map[string]string{"stopReason": "end_turn"})
		}
	}
}

func TestACPClient(t *testing.T) {
	dir := t.TempDir()
	req := Request{Spec: Spec{Kind: ACP, Command: os.Args[0], Env: map[string]string{"OPSDECK_FAKE_ACP": "1"}},
		Prompt: "hello", System: "SYS", MCPURL: "http://127.0.0.1:1/mcp/", MCPToken: "tok", WorkDir: dir}
	var events []Event
	res, err := Run(context.Background(), req, func(e Event) { events = append(events, e) })
	if err != nil {
		t.Fatal(err)
	}
	if res.Resume != "sess-1" {
		t.Fatalf("resume id: %q", res.Resume)
	}
	var texts, notices []string
	for _, e := range events {
		switch e.Kind {
		case "text":
			texts = append(texts, e.Text)
		case "notice":
			notices = append(notices, e.Text)
		}
	}
	all := strings.Join(texts, "\n")
	for _, want := range []string{`perm1={"outcome":{"optionId":"a","outcome":"selected"}}`, `perm2={"outcome":{"optionId":"r","outcome":"selected"}}`,
		`"code":-32601`, `prompt:<opsdeck-context>`, `"type":"http"`, `"Bearer tok"`} {
		if !strings.Contains(all, want) {
			t.Errorf("missing %s in:\n%s", want, all)
		}
	}
	if len(texts) != 2 || texts[0] != "Checking " {
		t.Errorf("text segments should split at tool calls: %q", texts)
	}
	if len(notices) != 1 || !strings.Contains(notices[0], "Shell") {
		t.Errorf("blocked tool notice: %v", notices)
	}

	// Second turn loads the session; replayed history is not shown again and
	// the context is not re-sent.
	req.Resume = res.Resume
	events = nil
	if _, err := Run(context.Background(), req, func(e Event) { events = append(events, e) }); err != nil {
		t.Fatal(err)
	}
	for _, e := range events {
		if strings.Contains(e.Text, "OLD HISTORY") {
			t.Fatal("replayed history leaked into the timeline")
		}
		if e.Kind == "text" && strings.Contains(e.Text, "<opsdeck-context>") {
			t.Fatal("context re-sent on a loaded session")
		}
	}
}
