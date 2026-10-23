// Package llm is a small provider-neutral layer over model APIs: Anthropic
// (official SDK) and any OpenAI-compatible Chat Completions endpoint
// (OpenAI, OpenRouter, Ollama, LM Studio, vLLM, ...).
package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"opsdeck/internal/config"
)

// Block is one piece of message content.
type Block struct {
	Type string `json:"type"` // text | thinking | redacted_thinking | tool_use | tool_result

	Text      string `json:"text,omitempty"`      // text, thinking
	Signature string `json:"signature,omitempty"` // thinking
	Data      string `json:"data,omitempty"`      // redacted_thinking

	ID    string          `json:"id,omitempty"`    // tool_use
	Name  string          `json:"name,omitempty"`  // tool_use
	Input json.RawMessage `json:"input,omitempty"` // tool_use

	ToolUseID string `json:"toolUseId,omitempty"` // tool_result
	Content   string `json:"content,omitempty"`   // tool_result
	IsError   bool   `json:"isError,omitempty"`   // tool_result

	// Provider that produced a thinking block; other providers drop it.
	Provider string `json:"provider,omitempty"`
}

// Message is a conversation turn. Role is "user" or "assistant".
type Message struct {
	Role    string  `json:"role"`
	Content []Block `json:"content"`
}

// Tool is a client tool definition.
type Tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Schema      map[string]any `json:"schema"` // JSON Schema object
}

type Request struct {
	Model     string
	System    string
	Messages  []Message
	Tools     []Tool
	MaxTokens int
}

type Usage struct {
	InputTokens  int `json:"inputTokens"`
	OutputTokens int `json:"outputTokens"`
	CacheRead    int `json:"cacheRead"`
}

type Response struct {
	Content    []Block
	StopReason string // end_turn | tool_use | max_tokens | refusal | ...
	Usage      Usage
	Refusal    string // explanation when StopReason == refusal
}

// Provider streams one model turn, calling onText with text deltas.
type Provider interface {
	Chat(ctx context.Context, req Request, onText func(string)) (*Response, error)
}

// KeyFunc resolves an API key reference (vault://...) at call time.
type KeyFunc func(ref string) (string, error)

// Router picks the provider for a "providerId/model" id.
type Router struct {
	providers map[string]config.Provider
	key       KeyFunc
}

func NewRouter(providers []config.Provider, key KeyFunc) *Router {
	m := map[string]config.Provider{}
	for _, p := range providers {
		m[p.ID] = p
	}
	return &Router{providers: m, key: key}
}

// Resolve returns the provider and bare model name of "providerId/model".
func (r *Router) Resolve(id string) (Provider, string, error) {
	pid, model, ok := strings.Cut(id, "/")
	if !ok {
		return nil, "", fmt.Errorf("model id %q must be <provider>/<model>", id)
	}
	p, ok := r.providers[pid]
	if !ok {
		return nil, "", fmt.Errorf("unknown model provider %q (configure it in Settings)", pid)
	}
	apiKey := ""
	if p.APIKeyRef != "" {
		k, err := r.key(p.APIKeyRef)
		if err != nil {
			return nil, "", fmt.Errorf("provider %s API key: %w", p.Name, err)
		}
		apiKey = k
	}
	switch p.Type {
	case "anthropic":
		return newAnthropic(p, apiKey), model, nil
	case "openai":
		return newOpenAI(p, apiKey), model, nil
	}
	return nil, "", fmt.Errorf("provider %s has unknown type %q", p.ID, p.Type)
}

// Lookup returns the provider config and bare model of "providerId/model".
func (r *Router) Lookup(id string) (config.Provider, string, bool) {
	pid, model, ok := strings.Cut(id, "/")
	if !ok {
		return config.Provider{}, "", false
	}
	p, ok := r.providers[pid]
	return p, model, ok
}

// ModelInfo is listed for model pickers.
type ModelInfo struct {
	ID           string `json:"id"`
	Provider     string `json:"provider"`
	ProviderName string `json:"providerName"`
	Model        string `json:"model"`
	Name         string `json:"name"` // the model's display name
	Label        string `json:"label"`
}

func ListModels(providers []config.Provider) []ModelInfo {
	out := []ModelInfo{}
	for _, p := range providers {
		for _, m := range p.Models {
			name := p.ModelNames[m]
			if name == "" {
				name = m
			}
			out = append(out, ModelInfo{ID: p.ID + "/" + m, Provider: p.ID, ProviderName: p.Name, Model: m, Name: name, Label: p.Name + " · " + name})
		}
	}
	return out
}
