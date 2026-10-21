package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"opsdeck/internal/config"
)

func TestAnthropicStreamingToolUse(t *testing.T) {
	var bodies []map[string]any
	var betas []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var m map[string]any
		json.Unmarshal(b, &m)
		bodies = append(bodies, m)
		betas = append(betas, r.Header.Get("anthropic-beta"))
		if r.Header.Get("x-api-key") != "k-test" {
			w.WriteHeader(401)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		ev := func(typ, data string) { fmt.Fprintf(w, "event: %s\ndata: %s\n\n", typ, data) }
		ev("message_start", `{"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"claude-opus-5-5","content":[],"stop_reason":null,"usage":{"input_tokens":12,"output_tokens":1,"cache_read_input_tokens":5}}}`)
		ev("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":"","signature":""}}`)
		ev("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"sig-abc"}}`)
		ev("content_block_stop", `{"type":"content_block_stop","index":0}`)
		ev("content_block_start", `{"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}`)
		ev("content_block_delta", `{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"Let me "}}`)
		ev("content_block_delta", `{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"check."}}`)
		ev("content_block_stop", `{"type":"content_block_stop","index":1}`)
		ev("content_block_start", `{"type":"content_block_start","index":2,"content_block":{"type":"tool_use","id":"toolu_1","name":"exec","input":{}}}`)
		ev("content_block_delta", `{"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"{\"target\":\"ap"}}`)
		ev("content_block_delta", `{"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"p-1\",\"command\":\"uptime\"}"}}`)
		ev("content_block_stop", `{"type":"content_block_stop","index":2}`)
		ev("message_delta", `{"type":"message_delta","delta":{"stop_reason":"tool_use","stop_sequence":null},"usage":{"output_tokens":40}}`)
		ev("message_stop", `{"type":"message_stop"}`)
	}))
	defer srv.Close()

	p := newAnthropic(config.Provider{ID: "a", Type: "anthropic", BaseURL: srv.URL}, "k-test")
	var streamed strings.Builder
	tools := []Tool{{Name: "exec", Description: "run", Schema: map[string]any{"type": "object", "properties": map[string]any{"target": map[string]any{"type": "string"}}, "required": []string{"target"}}}}
	resp, err := p.Chat(context.Background(), Request{Model: "claude-opus-5-5", System: "sys", Tools: tools,
		Messages: []Message{{Role: "user", Content: []Block{{Type: "text", Text: "hi"}}}}}, func(s string) { streamed.WriteString(s) })
	if err != nil {
		t.Fatal(err)
	}
	if streamed.String() != "Let me check." || resp.StopReason != "tool_use" {
		t.Fatalf("stream %q stop %s", streamed.String(), resp.StopReason)
	}
	if len(resp.Content) != 3 || resp.Content[0].Signature != "sig-abc" || resp.Content[2].Name != "exec" {
		t.Fatalf("content: %+v", resp.Content)
	}
	var in map[string]string
	json.Unmarshal(resp.Content[2].Input, &in)
	if in["target"] != "app-1" || in["command"] != "uptime" {
		t.Fatalf("input: %s", resp.Content[2].Input)
	}
	if resp.Usage.InputTokens != 17 || resp.Usage.OutputTokens != 40 {
		t.Fatalf("usage: %+v", resp.Usage)
	}

	// Request shape: effort, eager streaming, cache control; no fallback on a non-official base URL.
	req := bodies[0]
	if oc, _ := req["output_config"].(map[string]any); oc["effort"] != "high" {
		t.Fatalf("effort missing: %v", req["output_config"])
	}
	tool := req["tools"].([]any)[0].(map[string]any)
	if tool["eager_input_streaming"] != true {
		t.Fatalf("eager_input_streaming missing: %v", tool)
	}
	if _, ok := req["fallbacks"]; ok {
		t.Fatal("fallbacks must only be sent to the official API")
	}
	if req["stream"] != true {
		t.Fatal("expected streaming")
	}

	// Second turn replays the thinking block unchanged and sends tool results.
	history := []Message{
		{Role: "user", Content: []Block{{Type: "text", Text: "hi"}}},
		{Role: "assistant", Content: resp.Content},
		{Role: "user", Content: []Block{{Type: "tool_result", ToolUseID: "toolu_1", Content: "up 3 days"}}},
	}
	p.Chat(context.Background(), Request{Model: "claude-opus-5-5", Messages: history}, nil)
	msgs := bodies[1]["messages"].([]any)
	asst := msgs[1].(map[string]any)["content"].([]any)
	if asst[0].(map[string]any)["type"] != "thinking" || asst[0].(map[string]any)["signature"] != "sig-abc" {
		t.Fatalf("thinking block not replayed: %v", asst[0])
	}
	tr := msgs[2].(map[string]any)["content"].([]any)[0].(map[string]any)
	if tr["type"] != "tool_result" || tr["tool_use_id"] != "toolu_1" {
		t.Fatalf("tool result: %v", tr)
	}

	// A different provider instance drops thinking blocks it did not produce.
	other := newAnthropic(config.Provider{ID: "b", Type: "anthropic", BaseURL: srv.URL}, "k-test")
	other.Chat(context.Background(), Request{Model: "claude-opus-5-5", Messages: history}, nil)
	asst2 := bodies[2]["messages"].([]any)[1].(map[string]any)["content"].([]any)
	if asst2[0].(map[string]any)["type"] == "thinking" {
		t.Fatal("foreign thinking block should be dropped")
	}

	// The official endpoint gets the server-side refusal fallback.
	official := newAnthropic(config.Provider{ID: "c", Type: "anthropic"}, "k")
	if !official.official {
		t.Fatal("empty base URL should be the official API")
	}
	p.official = true
	p.Chat(context.Background(), Request{Model: "claude-opus-5-5", Messages: history[:1]}, nil)
	if bodies[3]["fallbacks"] != "default" || !strings.Contains(betas[3], "server-side-fallback-2026-07-01") {
		t.Fatalf("fallback not requested: %v / %q", bodies[3]["fallbacks"], betas[3])
	}
}
