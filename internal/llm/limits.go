package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/anthropics/anthropic-sdk-go"

	"opsdeck/internal/config"
)

// Limit is one limit a provider reported: a rate limit from the headers of
// a model response, or a quota from the provider's own API. What the
// provider did not send stays nil or empty; nothing is estimated.
type Limit struct {
	Name      string   `json:"name"` // requests | tokens | input tokens | output tokens | project tokens | credits
	Limit     *float64 `json:"limit,omitempty"`
	Remaining *float64 `json:"remaining,omitempty"`
	Used      *float64 `json:"used,omitempty"`
	Reset     string   `json:"reset,omitempty"`  // RFC 3339: when the limit is replenished
	Period    string   `json:"period,omitempty"` // credits: how often the limit resets (daily, weekly, monthly)
}

// rateHeaders are the rate limit headers of each API family, as documented
// by the provider: Anthropic sends RFC 3339 reset times, OpenAI-compatible
// APIs a duration ("1s", "6m0s").
var rateHeaders = []struct{ name, limit, remaining, reset string }{
	{"requests", "anthropic-ratelimit-requests-limit", "anthropic-ratelimit-requests-remaining", "anthropic-ratelimit-requests-reset"},
	{"tokens", "anthropic-ratelimit-tokens-limit", "anthropic-ratelimit-tokens-remaining", "anthropic-ratelimit-tokens-reset"},
	{"input tokens", "anthropic-ratelimit-input-tokens-limit", "anthropic-ratelimit-input-tokens-remaining", "anthropic-ratelimit-input-tokens-reset"},
	{"output tokens", "anthropic-ratelimit-output-tokens-limit", "anthropic-ratelimit-output-tokens-remaining", "anthropic-ratelimit-output-tokens-reset"},
	{"requests", "x-ratelimit-limit-requests", "x-ratelimit-remaining-requests", "x-ratelimit-reset-requests"},
	{"tokens", "x-ratelimit-limit-tokens", "x-ratelimit-remaining-tokens", "x-ratelimit-reset-tokens"},
	{"project tokens", "x-ratelimit-limit-project-tokens", "x-ratelimit-remaining-project-tokens", "x-ratelimit-reset-project-tokens"},
}

// ParseRateLimits reads the rate limit headers of a response. A limit is
// listed only when the response carries its limit or remaining header.
func ParseRateLimits(h http.Header, now time.Time) []Limit {
	var out []Limit
	for _, rh := range rateHeaders {
		l := Limit{Name: rh.name, Limit: headerNumber(h, rh.limit), Remaining: headerNumber(h, rh.remaining)}
		if l.Limit == nil && l.Remaining == nil {
			continue
		}
		l.Reset = resetTime(h.Get(rh.reset), now)
		out = append(out, l)
	}
	return out
}

func headerNumber(h http.Header, name string) *float64 {
	v := strings.TrimSpace(h.Get(name))
	if v == "" {
		return nil
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return nil
	}
	return &f
}

// resetTime turns a reset header (an RFC 3339 time or a duration from now)
// into an RFC 3339 time; "" when it is neither.
func resetTime(v string, now time.Time) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return ""
	}
	if t, err := time.Parse(time.RFC3339, v); err == nil {
		return t.UTC().Format(time.RFC3339)
	}
	if d, err := time.ParseDuration(v); err == nil && d >= 0 {
		return now.Add(d).UTC().Format(time.RFC3339)
	}
	return ""
}

// ProviderLimits are the limits a provider reported last.
type ProviderLimits struct {
	Limits   []Limit   `json:"limits"`
	Observed time.Time `json:"observed"`
}

// reported holds the rate limits each provider sent with its latest
// response, by provider id, for as long as opsdeck runs.
var reported = struct {
	sync.Mutex
	m map[string]ProviderLimits
}{m: map[string]ProviderLimits{}}

func recordRateLimits(providerID string, h http.Header) {
	limits := ParseRateLimits(h, time.Now())
	if providerID == "" || len(limits) == 0 {
		return
	}
	reported.Lock()
	reported.m[providerID] = ProviderLimits{Limits: limits, Observed: time.Now().UTC()}
	reported.Unlock()
}

// ReportedLimits returns the rate limits the provider sent with its latest
// response; false when it has sent none since opsdeck started.
func ReportedLimits(providerID string) (ProviderLimits, bool) {
	reported.Lock()
	defer reported.Unlock()
	l, ok := reported.m[providerID]
	return l, ok
}

// ProviderReport is what the chat shows for one configured provider.
type ProviderReport struct {
	ID       string     `json:"id"`
	Name     string     `json:"name"`
	Type     string     `json:"type"`
	Limits   []Limit    `json:"limits"` // empty: the provider reported none
	Observed *time.Time `json:"observed,omitempty"`
	Error    string     `json:"error,omitempty"` // asking the provider for its quota failed
}

// Limits reports every configured provider's limits: the rate limits of
// its latest response and, where the provider has an API for it, the
// key's quota (OpenRouter).
func (r *Router) Limits(ctx context.Context) []ProviderReport {
	out := []ProviderReport{}
	for _, id := range r.order {
		p := r.providers[id]
		rep := ProviderReport{ID: p.ID, Name: p.Name, Type: p.Type, Limits: []Limit{}}
		if l, ok := ReportedLimits(p.ID); ok {
			rep.Limits = append(rep.Limits, l.Limits...)
			t := l.Observed
			rep.Observed = &t
		}
		if p.Type == "openai" && isOpenRouter(p.BaseURL) {
			apiKey, err := r.apiKey(p)
			if err == nil {
				var q []Limit
				q, err = cachedQuota(ctx, p, apiKey)
				rep.Limits = append(rep.Limits, q...)
			}
			if err != nil {
				rep.Error = err.Error()
			}
		}
		out = append(out, rep)
	}
	return out
}

func isOpenRouter(baseURL string) bool { return strings.Contains(baseURL, "openrouter.ai") }

type cached[T any] struct {
	v       T
	err     error
	expires time.Time
}

var quotaCache = struct {
	sync.Mutex
	m map[string]cached[[]Limit]
}{m: map[string]cached[[]Limit]{}}

// cachedQuota asks OpenRouter for the key's credits at most once a minute.
func cachedQuota(ctx context.Context, p config.Provider, apiKey string) ([]Limit, error) {
	key := p.ID + "|" + p.BaseURL
	quotaCache.Lock()
	c, ok := quotaCache.m[key]
	quotaCache.Unlock()
	if ok && time.Now().Before(c.expires) {
		return c.v, c.err
	}
	v, err := openRouterQuota(ctx, newOpenAI(p, apiKey))
	quotaCache.Lock()
	quotaCache.m[key] = cached[[]Limit]{v: v, err: err, expires: time.Now().Add(time.Minute)}
	quotaCache.Unlock()
	return v, err
}

// openRouterQuota reads the key's credit limit from OpenRouter's GET /key:
// limit (null = unlimited), limit_remaining, usage and limit_reset.
func openRouterQuota(ctx context.Context, o *openaiProvider) ([]Limit, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var body struct {
		Data struct {
			Limit          *float64 `json:"limit"`
			LimitRemaining *float64 `json:"limit_remaining"`
			LimitReset     *string  `json:"limit_reset"`
			Usage          *float64 `json:"usage"`
		} `json:"data"`
	}
	if err := o.getJSON(ctx, "/key", &body); err != nil {
		return nil, err
	}
	d := body.Data
	l := Limit{Name: "credits", Limit: d.Limit, Remaining: d.LimitRemaining, Used: d.Usage}
	if d.LimitReset != nil {
		l.Period = *d.LimitReset
	}
	return []Limit{l}, nil
}

func (o *openaiProvider) getJSON(ctx context.Context, path string, v any) error {
	req, err := http.NewRequestWithContext(ctx, "GET", o.baseURL+path, nil)
	if err != nil {
		return err
	}
	if o.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+o.apiKey)
	}
	res, err := o.http.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 32<<20))
	if res.StatusCode/100 != 2 {
		return fmt.Errorf("GET %s: %s", path, res.Status)
	}
	if err := json.Unmarshal(b, v); err != nil {
		return fmt.Errorf("GET %s: %v", path, err)
	}
	return nil
}

var windowCache = struct {
	sync.Mutex
	m map[string]cached[int]
}{m: map[string]cached[int]{}}

// ContextWindow returns the context window, in input tokens, that the
// provider reports for a "providerId/model" id; 0 when it reports none.
// Answers are kept for a few hours, failures for a few minutes.
func (r *Router) ContextWindow(ctx context.Context, id string) (int, error) {
	p, model, ok := r.Lookup(id)
	if !ok || (p.Type != "anthropic" && p.Type != "openai") {
		return 0, nil
	}
	key := p.ID + "|" + p.Type + "|" + p.BaseURL + "|" + model
	windowCache.Lock()
	c, hit := windowCache.m[key]
	windowCache.Unlock()
	if hit && time.Now().Before(c.expires) {
		return c.v, c.err
	}
	apiKey, err := r.apiKey(p)
	if err != nil {
		return 0, err // the vault may be locked: ask again next time
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	n, err := contextWindow(ctx, p, apiKey, model)
	ttl := 6 * time.Hour
	if err != nil {
		ttl = 5 * time.Minute
	}
	windowCache.Lock()
	windowCache.m[key] = cached[int]{v: n, err: err, expires: time.Now().Add(ttl)}
	windowCache.Unlock()
	return n, err
}

// contextWindow asks the provider: Anthropic's Models API (max_input_tokens),
// or the OpenAI-compatible GET /models, where some servers list a model's
// context: context_length (OpenRouter), max_model_len (vLLM),
// context_window (Groq). OpenAI itself lists none.
func contextWindow(ctx context.Context, p config.Provider, apiKey, model string) (int, error) {
	switch p.Type {
	case "anthropic":
		a := newAnthropic(p, apiKey)
		info, err := a.client.Models.Get(ctx, model, anthropic.ModelGetParams{})
		if err != nil {
			return 0, describeAnthropicError(err)
		}
		if !info.JSON.MaxInputTokens.Valid() {
			return 0, nil
		}
		return int(info.MaxInputTokens), nil
	case "openai":
		var body struct {
			Data []map[string]any `json:"data"`
		}
		if err := newOpenAI(p, apiKey).getJSON(ctx, "/models", &body); err != nil {
			return 0, err
		}
		for _, m := range body.Data {
			if id, _ := m["id"].(string); id != model {
				continue
			}
			for _, f := range []string{"context_length", "max_model_len", "context_window"} {
				if n, ok := m[f].(float64); ok && n > 0 {
					return int(n), nil
				}
			}
			if tp, ok := m["top_provider"].(map[string]any); ok {
				if n, ok := tp["context_length"].(float64); ok && n > 0 {
					return int(n), nil
				}
			}
			return 0, nil
		}
		return 0, nil
	}
	return 0, nil
}
