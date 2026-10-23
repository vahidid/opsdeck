package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/anthropics/anthropic-sdk-go"

	"opsdeck/internal/config"
)

// RemoteModel is a model a provider's API lists.
type RemoteModel struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// FetchModels asks the provider's API which models the key can use:
// Anthropic's Models API or the OpenAI-compatible GET /models (OpenAI,
// OpenRouter, Ollama, LM Studio, vLLM...).
func FetchModels(ctx context.Context, p config.Provider, apiKey string) ([]RemoteModel, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	switch p.Type {
	case "anthropic":
		a := newAnthropic(p, apiKey)
		out := []RemoteModel{}
		it := a.client.Models.ListAutoPaging(ctx, anthropic.ModelListParams{Limit: anthropic.Int(1000)})
		for it.Next() {
			m := it.Current()
			out = append(out, RemoteModel{ID: m.ID, Name: firstNonEmpty(m.DisplayName, m.ID)})
		}
		if err := it.Err(); err != nil {
			return nil, describeAnthropicError(err)
		}
		return out, nil
	case "openai":
		return fetchOpenAIModels(ctx, newOpenAI(p, apiKey))
	}
	return nil, fmt.Errorf("provider type %q does not list models", p.Type)
}

func fetchOpenAIModels(ctx context.Context, o *openaiProvider) ([]RemoteModel, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", o.baseURL+"/models", nil)
	if err != nil {
		return nil, err
	}
	if o.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+o.apiKey)
	}
	res, err := o.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 32<<20))
	if res.StatusCode/100 != 2 {
		return nil, fmt.Errorf("GET %s/models: %s %s", o.baseURL, res.Status, strings.TrimSpace(string(b[:min(len(b), 300)])))
	}
	var body struct {
		Data []struct {
			ID          string `json:"id"`
			Name        string `json:"name"`        // OpenRouter
			Description string `json:"description"` // OpenRouter
			OwnedBy     string `json:"owned_by"`
		} `json:"data"`
	}
	if err := json.Unmarshal(b, &body); err != nil {
		return nil, fmt.Errorf("GET %s/models: %v", o.baseURL, err)
	}
	out := []RemoteModel{}
	for _, m := range body.Data {
		if m.ID == "" {
			continue
		}
		desc := m.Description
		if r := []rune(desc); len(r) > 200 {
			desc = string(r[:200]) + "…"
		}
		if desc == "" && m.OwnedBy != "" {
			desc = "by " + m.OwnedBy
		}
		out = append(out, RemoteModel{ID: m.ID, Name: firstNonEmpty(m.Name, m.ID), Description: desc})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func firstNonEmpty(s ...string) string {
	for _, v := range s {
		if v != "" {
			return v
		}
	}
	return ""
}
