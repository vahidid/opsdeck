package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/anthropics/anthropic-sdk-go/packages/param"
	"github.com/anthropics/anthropic-sdk-go/shared/constant"

	"opsdeck/internal/config"
)

type anthropicProvider struct {
	client   anthropic.Client
	id       string
	official bool
}

func newAnthropic(p config.Provider, apiKey string) *anthropicProvider {
	opts := []option.RequestOption{option.WithAPIKey(apiKey), option.WithMaxRetries(3)}
	official := p.BaseURL == "" || strings.Contains(p.BaseURL, "api.anthropic.com")
	if p.BaseURL != "" {
		opts = append(opts, option.WithBaseURL(p.BaseURL))
	}
	return &anthropicProvider{client: anthropic.NewClient(opts...), id: "anthropic:" + p.ID, official: official}
}

// Models of the current generation take output_config.effort and the
// server-side refusal fallback.
var (
	effortModels   = regexp.MustCompile(`^claude-(opus|sonnet|haiku|fable|mythos)-5`)
	fallbackModels = map[string]bool{"claude-fable-5-1": true, "claude-opus-5-5": true, "claude-opus-5": true, "claude-sonnet-5-5": true}
)

func (a *anthropicProvider) Chat(ctx context.Context, req Request, onText func(string)) (*Response, error) {
	maxTokens := int64(req.MaxTokens)
	if maxTokens <= 0 {
		maxTokens = 64000
	}
	params := anthropic.BetaMessageNewParams{
		Model:     anthropic.Model(req.Model),
		MaxTokens: maxTokens,
		Messages:  a.messages(req.Messages),
		// Auto-cache the growing conversation prefix.
		CacheControl: anthropic.BetaCacheControlEphemeralParam{},
	}
	if req.System != "" {
		params.System = []anthropic.BetaTextBlockParam{{Text: req.System, CacheControl: anthropic.BetaCacheControlEphemeralParam{}}}
	}
	for _, t := range req.Tools {
		props, _ := t.Schema["properties"].(map[string]any)
		var required []string
		if r, ok := t.Schema["required"].([]string); ok {
			required = r
		}
		tp := anthropic.BetaToolParam{
			Name:                t.Name,
			Description:         param.NewOpt(t.Description),
			InputSchema:         anthropic.BetaToolInputSchemaParam{Properties: props, Required: required},
			EagerInputStreaming: param.NewOpt(true),
		}
		params.Tools = append(params.Tools, anthropic.BetaToolUnionParam{OfTool: &tp})
	}
	if effortModels.MatchString(req.Model) {
		// Infrastructure work is multi-step and tool heavy.
		params.OutputConfig = anthropic.BetaOutputConfigParam{Effort: anthropic.BetaOutputConfigEffort("high")}
	}
	if a.official && fallbackModels[req.Model] {
		params.Fallbacks = anthropic.BetaFallbacksParamUnion{OfDefault: constant.ValueOf[constant.Default]()}
		params.Betas = append(params.Betas, anthropic.AnthropicBetaServerSideFallback2026_07_01)
	}

	stream := a.client.Beta.Messages.NewStreaming(ctx, params)
	msg := anthropic.BetaMessage{}
	for stream.Next() {
		ev := stream.Current()
		if err := msg.Accumulate(ev); err != nil {
			return nil, err
		}
		if d, ok := ev.AsAny().(anthropic.BetaRawContentBlockDeltaEvent); ok {
			if td, ok := d.Delta.AsAny().(anthropic.BetaTextDelta); ok && onText != nil {
				onText(td.Text)
			}
		}
	}
	if err := stream.Err(); err != nil {
		return nil, describeAnthropicError(err)
	}

	resp := &Response{
		StopReason: string(msg.StopReason),
		Usage: Usage{
			InputTokens:  int(msg.Usage.InputTokens + msg.Usage.CacheReadInputTokens + msg.Usage.CacheCreationInputTokens),
			OutputTokens: int(msg.Usage.OutputTokens),
			CacheRead:    int(msg.Usage.CacheReadInputTokens),
		},
	}
	if msg.StopReason == anthropic.BetaStopReasonRefusal {
		resp.Refusal = msg.StopDetails.Explanation
		if resp.Refusal == "" {
			resp.Refusal = "the model declined this request"
		}
	}
	for _, b := range msg.Content {
		switch v := b.AsAny().(type) {
		case anthropic.BetaTextBlock:
			resp.Content = append(resp.Content, Block{Type: "text", Text: v.Text})
		case anthropic.BetaThinkingBlock:
			resp.Content = append(resp.Content, Block{Type: "thinking", Text: v.Thinking, Signature: v.Signature, Provider: a.id})
		case anthropic.BetaRedactedThinkingBlock:
			resp.Content = append(resp.Content, Block{Type: "redacted_thinking", Data: v.Data, Provider: a.id})
		case anthropic.BetaToolUseBlock:
			raw := json.RawMessage(v.JSON.Input.Raw())
			if len(raw) == 0 {
				raw = json.RawMessage("{}")
			}
			resp.Content = append(resp.Content, Block{Type: "tool_use", ID: v.ID, Name: v.Name, Input: raw})
		}
	}
	return resp, nil
}

func (a *anthropicProvider) messages(in []Message) []anthropic.BetaMessageParam {
	out := make([]anthropic.BetaMessageParam, 0, len(in))
	for _, m := range in {
		var blocks []anthropic.BetaContentBlockParamUnion
		for _, b := range m.Content {
			switch b.Type {
			case "text":
				if b.Text != "" {
					blocks = append(blocks, anthropic.NewBetaTextBlock(b.Text))
				}
			case "thinking":
				// Thinking blocks are passed back unchanged, but only to the
				// provider that produced them.
				if b.Provider == a.id {
					blocks = append(blocks, anthropic.NewBetaThinkingBlock(b.Signature, b.Text))
				}
			case "redacted_thinking":
				if b.Provider == a.id {
					blocks = append(blocks, anthropic.NewBetaRedactedThinkingBlock(b.Data))
				}
			case "tool_use":
				var input any = map[string]any{}
				if len(b.Input) > 0 {
					var v any
					if json.Unmarshal(b.Input, &v) == nil && v != nil {
						input = v
					}
				}
				blocks = append(blocks, anthropic.NewBetaToolUseBlock(b.ID, input, b.Name))
			case "tool_result":
				content := b.Content
				if content == "" {
					content = "(no output)"
				}
				blocks = append(blocks, anthropic.NewBetaToolResultBlock(b.ToolUseID, content, b.IsError))
			}
		}
		if len(blocks) == 0 {
			continue
		}
		if m.Role == "assistant" {
			out = append(out, anthropic.BetaMessageParam{Role: anthropic.BetaMessageParamRoleAssistant, Content: blocks})
		} else {
			out = append(out, anthropic.NewBetaUserMessage(blocks...))
		}
	}
	return out
}

func describeAnthropicError(err error) error {
	var apiErr *anthropic.Error
	if errors.As(err, &apiErr) {
		switch apiErr.StatusCode {
		case 401:
			return fmt.Errorf("Anthropic API: invalid API key (check the provider's key in Settings)")
		case 404:
			return fmt.Errorf("Anthropic API: model not found: %v", apiErr)
		case 429:
			return fmt.Errorf("Anthropic API: rate limited, try again shortly")
		}
		return fmt.Errorf("Anthropic API error %d: %v", apiErr.StatusCode, apiErr)
	}
	return err
}
