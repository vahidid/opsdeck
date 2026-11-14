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
	Type string `json:"type"` // text | thinking | redacted_thinking | tool_use | tool_result | image | document

	Text      string `json:"text,omitempty"`      // text, thinking
	Signature string `json:"signature,omitempty"` // thinking
	Data      string `json:"data,omitempty"`      // redacted_thinking; image, document: base64 (filled in for a request, not stored)

	ID    string          `json:"id,omitempty"`    // tool_use
	Name  string          `json:"name,omitempty"`  // tool_use; image, document: the file name
	Input json.RawMessage `json:"input,omitempty"` // tool_use

	ToolUseID string `json:"toolUseId,omitempty"` // tool_result
	Content   string `json:"content,omitempty"`   // tool_result
	IsError   bool   `json:"isError,omitempty"`   // tool_result

	// Provider that produced a thinking block; other providers drop it.
	Provider string `json:"provider,omitempty"`

	// image, document: a file the user attached. Source names where its
	// bytes are kept (an attachment id); they are read into Data just
	// before a request, so transcripts stay small.
	MediaType string `json:"mediaType,omitempty"`
	Source    string `json:"source,omitempty"`
}

// placeholder is the text a provider sends for an attachment whose bytes
// it does not have (or cannot send).
func (b Block) placeholder() string {
	return "[attached " + b.Type + ": " + b.Name + " — not available to the model here]"
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
	order     []string // provider ids as configured
	key       KeyFunc
}

func NewRouter(providers []config.Provider, key KeyFunc) *Router {
	m := map[string]config.Provider{}
	var order []string
	for _, p := range providers {
		if _, dup := m[p.ID]; !dup {
			order = append(order, p.ID)
		}
		m[p.ID] = p
	}
	return &Router{providers: m, order: order, key: key}
}

// apiKey resolves a provider's API key; "" when it has none (Ollama).
func (r *Router) apiKey(p config.Provider) (string, error) {
	if p.APIKeyRef == "" {
		return "", nil
	}
	k, err := r.key(p.APIKeyRef)
	if err != nil {
		return "", fmt.Errorf("provider %s API key: %w", p.Name, err)
	}
	return k, nil
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
	apiKey, err := r.apiKey(p)
	if err != nil {
		return nil, "", err
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
