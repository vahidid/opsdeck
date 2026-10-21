// Package fakellm is a scripted OpenAI-compatible streaming endpoint for tests.
package fakellm

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
)

// Turn is one scripted assistant response.
type Turn struct {
	Text  string
	Tools []ToolCall
}

type ToolCall struct {
	Name string
	Args map[string]any
}

// Server answers the n-th assistant turn of a conversation with Script[n].
type Server struct {
	*httptest.Server
	Script []Turn

	mu       sync.Mutex
	Requests []map[string]any
}

func New(script []Turn) *Server {
	s := &Server{Script: script}
	s.Server = httptest.NewServer(http.HandlerFunc(s.handle))
	return s
}

func (s *Server) handle(w http.ResponseWriter, r *http.Request) {
	var req map[string]any
	json.NewDecoder(r.Body).Decode(&req)
	s.mu.Lock()
	s.Requests = append(s.Requests, req)
	s.mu.Unlock()
	msgs, _ := req["messages"].([]any)
	n := 0
	for _, m := range msgs {
		if mm, ok := m.(map[string]any); ok && mm["role"] == "assistant" {
			n++
		}
	}
	turn := Turn{Text: "(script exhausted)"}
	if n < len(s.Script) {
		turn = s.Script[n]
	}
	w.Header().Set("Content-Type", "text/event-stream")
	send := func(v any) {
		b, _ := json.Marshal(v)
		fmt.Fprintf(w, "data: %s\n\n", b)
		w.(http.Flusher).Flush()
	}
	if turn.Text != "" {
		// stream in two chunks
		half := len(turn.Text) / 2
		for _, part := range []string{turn.Text[:half], turn.Text[half:]} {
			send(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"content": part}}}})
		}
	}
	for i, tc := range turn.Tools {
		args, _ := json.Marshal(tc.Args)
		send(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"tool_calls": []any{map[string]any{
			"index": i, "id": fmt.Sprintf("call_%d_%d", n, i), "type": "function",
			"function": map[string]any{"name": tc.Name, "arguments": string(args)},
		}}}}}})
	}
	finish := "stop"
	if len(turn.Tools) > 0 {
		finish = "tool_calls"
	}
	send(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{}, "finish_reason": finish}}})
	send(map[string]any{"choices": []any{}, "usage": map[string]any{"prompt_tokens": 100, "completion_tokens": 20}})
	fmt.Fprint(w, "data: [DONE]\n\n")
}

// LastSystem returns the system prompt of the most recent request.
func (s *Server) LastSystem() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.Requests) == 0 {
		return ""
	}
	msgs, _ := s.Requests[len(s.Requests)-1]["messages"].([]any)
	for _, m := range msgs {
		if mm, ok := m.(map[string]any); ok && mm["role"] == "system" {
			c, _ := mm["content"].(string)
			return c
		}
	}
	return ""
}

// AllToolResults returns the content of every tool message seen in the last request.
func (s *Server) LastToolResults() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	if len(s.Requests) == 0 {
		return out
	}
	msgs, _ := s.Requests[len(s.Requests)-1]["messages"].([]any)
	for _, m := range msgs {
		if mm, ok := m.(map[string]any); ok && mm["role"] == "tool" {
			c, _ := mm["content"].(string)
			out = append(out, c)
		}
	}
	return out
}
