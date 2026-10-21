package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"opsdeck/internal/config"
)

// openaiProvider speaks the OpenAI Chat Completions streaming protocol, which
// most model servers implement (OpenAI, OpenRouter, Ollama, LM Studio, vLLM...).
type openaiProvider struct {
	baseURL string
	apiKey  string
	http    *http.Client
}

func newOpenAI(p config.Provider, apiKey string) *openaiProvider {
	base := strings.TrimRight(p.BaseURL, "/")
	if base == "" {
		base = "https://api.openai.com/v1"
	}
	return &openaiProvider{baseURL: base, apiKey: apiKey, http: &http.Client{Timeout: 30 * time.Minute}}
}

type oaMessage struct {
	Role       string       `json:"role"`
	Content    *string      `json:"content"`
	ToolCalls  []oaToolCall `json:"tool_calls,omitempty"`
	ToolCallID string       `json:"tool_call_id,omitempty"`
}

type oaToolCall struct {
	Index    int    `json:"index"`
	ID       string `json:"id,omitempty"`
	Type     string `json:"type,omitempty"`
	Function struct {
		Name      string `json:"name,omitempty"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

func strPtr(s string) *string { return &s }

func (o *openaiProvider) Chat(ctx context.Context, req Request, onText func(string)) (*Response, error) {
	msgs := []oaMessage{}
	if req.System != "" {
		msgs = append(msgs, oaMessage{Role: "system", Content: strPtr(req.System)})
	}
	for _, m := range req.Messages {
		if m.Role == "assistant" {
			am := oaMessage{Role: "assistant"}
			var text strings.Builder
			for _, b := range m.Content {
				switch b.Type {
				case "text":
					text.WriteString(b.Text)
				case "tool_use":
					tc := oaToolCall{ID: b.ID, Type: "function"}
					tc.Function.Name = b.Name
					tc.Function.Arguments = string(b.Input)
					am.ToolCalls = append(am.ToolCalls, tc)
				}
			}
			am.Content = strPtr(text.String())
			msgs = append(msgs, am)
			continue
		}
		var text strings.Builder
		for _, b := range m.Content {
			switch b.Type {
			case "tool_result":
				content := b.Content
				if b.IsError {
					content = "ERROR: " + content
				}
				msgs = append(msgs, oaMessage{Role: "tool", ToolCallID: b.ToolUseID, Content: strPtr(content)})
			case "text":
				text.WriteString(b.Text)
			}
		}
		if text.Len() > 0 {
			msgs = append(msgs, oaMessage{Role: "user", Content: strPtr(text.String())})
		}
	}
	body := map[string]any{
		"model":          req.Model,
		"messages":       msgs,
		"stream":         true,
		"stream_options": map[string]any{"include_usage": true},
	}
	if req.MaxTokens > 0 {
		body["max_tokens"] = req.MaxTokens
	}
	if len(req.Tools) > 0 {
		var tools []map[string]any
		for _, t := range req.Tools {
			tools = append(tools, map[string]any{"type": "function", "function": map[string]any{
				"name": t.Name, "description": t.Description, "parameters": t.Schema,
			}})
		}
		body["tools"] = tools
	}
	b, _ := json.Marshal(body)
	hreq, err := http.NewRequestWithContext(ctx, "POST", o.baseURL+"/chat/completions", bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	hreq.Header.Set("Content-Type", "application/json")
	if o.apiKey != "" {
		hreq.Header.Set("Authorization", "Bearer "+o.apiKey)
	}
	res, err := o.http.Do(hreq)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode >= 300 {
		msg, _ := io.ReadAll(io.LimitReader(res.Body, 4096))
		return nil, fmt.Errorf("model API %s: %s", res.Status, strings.TrimSpace(string(msg)))
	}

	var text strings.Builder
	calls := map[int]*oaToolCall{}
	resp := &Response{StopReason: "end_turn"}
	sc := bufio.NewScanner(res.Body)
	sc.Buffer(make([]byte, 64*1024), 8*1024*1024)
	for sc.Scan() {
		line := sc.Text()
		data, ok := strings.CutPrefix(line, "data:")
		if !ok {
			continue
		}
		data = strings.TrimSpace(data)
		if data == "[DONE]" {
			break
		}
		var chunk struct {
			Choices []struct {
				Delta struct {
					Content   string       `json:"content"`
					ToolCalls []oaToolCall `json:"tool_calls"`
				} `json:"delta"`
				FinishReason string `json:"finish_reason"`
			} `json:"choices"`
			Usage *struct {
				PromptTokens     int `json:"prompt_tokens"`
				CompletionTokens int `json:"completion_tokens"`
			} `json:"usage"`
			Error *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue
		}
		if chunk.Error != nil {
			return nil, fmt.Errorf("model API: %s", chunk.Error.Message)
		}
		if chunk.Usage != nil {
			resp.Usage.InputTokens = chunk.Usage.PromptTokens
			resp.Usage.OutputTokens = chunk.Usage.CompletionTokens
		}
		for _, ch := range chunk.Choices {
			if ch.Delta.Content != "" {
				text.WriteString(ch.Delta.Content)
				if onText != nil {
					onText(ch.Delta.Content)
				}
			}
			for _, tc := range ch.Delta.ToolCalls {
				cur := calls[tc.Index]
				if cur == nil {
					cur = &oaToolCall{Index: tc.Index}
					calls[tc.Index] = cur
				}
				if tc.ID != "" {
					cur.ID = tc.ID
				}
				if tc.Function.Name != "" {
					cur.Function.Name += tc.Function.Name
				}
				cur.Function.Arguments += tc.Function.Arguments
			}
			switch ch.FinishReason {
			case "length":
				resp.StopReason = "max_tokens"
			case "content_filter":
				resp.StopReason = "refusal"
				resp.Refusal = "the provider's content filter stopped the response"
			}
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if text.Len() > 0 {
		resp.Content = append(resp.Content, Block{Type: "text", Text: text.String()})
	}
	idx := make([]int, 0, len(calls))
	for i := range calls {
		idx = append(idx, i)
	}
	sort.Ints(idx)
	for n, i := range idx {
		tc := calls[i]
		if tc.ID == "" {
			tc.ID = fmt.Sprintf("call_%d_%d", time.Now().UnixNano(), n)
		}
		args := strings.TrimSpace(tc.Function.Arguments)
		if args == "" {
			args = "{}"
		}
		resp.Content = append(resp.Content, Block{Type: "tool_use", ID: tc.ID, Name: tc.Function.Name, Input: json.RawMessage(args)})
	}
	if len(calls) > 0 && resp.StopReason == "end_turn" {
		resp.StopReason = "tool_use"
	}
	return resp, nil
}
