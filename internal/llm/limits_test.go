package llm

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"opsdeck/internal/config"
)

func TestParseRateLimits(t *testing.T) {
	now := time.Date(2026, 11, 14, 10, 0, 0, 0, time.UTC)
	h := http.Header{}
	h.Set("anthropic-ratelimit-requests-limit", "50")
	h.Set("anthropic-ratelimit-requests-remaining", "49")
	h.Set("anthropic-ratelimit-requests-reset", "2026-11-14T10:00:30+00:00")
	h.Set("anthropic-ratelimit-input-tokens-remaining", "29000")
	h.Set("x-ratelimit-limit-tokens", "150000")
	h.Set("x-ratelimit-remaining-tokens", "149984")
	h.Set("x-ratelimit-reset-tokens", "6m0s")
	h.Set("x-ratelimit-limit-requests", "not a number")
	got := ParseRateLimits(h, now)
	want := []string{
		"requests 50 49 2026-11-14T10:00:30Z",
		"input tokens - 29000 ",
		"tokens 150000 149984 2026-11-14T10:06:00Z",
	}
	if len(got) != len(want) {
		t.Fatalf("got %d limits: %+v", len(got), got)
	}
	for i, l := range got {
		if s := fmt.Sprintf("%s %s %s %s", l.Name, num(l.Limit), num(l.Remaining), l.Reset); s != want[i] {
			t.Errorf("limit %d = %q, want %q", i, s, want[i])
		}
	}
	if ParseRateLimits(http.Header{}, now) != nil {
		t.Fatal("no headers must give no limits")
	}
}

func num(f *float64) string {
	if f == nil {
		return "-"
	}
	return fmt.Sprint(*f)
}

func TestResponsesRecordRateLimits(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("x-ratelimit-limit-requests", "60")
		w.Header().Set("x-ratelimit-remaining-requests", "59")
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer srv.Close()
	// a fresh record each run (go test -count=N)
	reported.Lock()
	delete(reported.m, "rec-test")
	delete(reported.m, "rec-anthropic")
	reported.Unlock()
	if _, ok := ReportedLimits("rec-test"); ok {
		t.Fatal("limits before any response")
	}
	p := newOpenAI(config.Provider{ID: "rec-test", Type: "openai", BaseURL: srv.URL}, "")
	if _, err := p.Chat(context.Background(), Request{Model: "m", Messages: []Message{{Role: "user", Content: []Block{{Type: "text", Text: "hi"}}}}}, nil); err != nil {
		t.Fatal(err)
	}
	l, ok := ReportedLimits("rec-test")
	if !ok || len(l.Limits) != 1 || *l.Limits[0].Limit != 60 || *l.Limits[0].Remaining != 59 || l.Observed.IsZero() {
		t.Fatalf("recorded: %+v %v", l, ok)
	}

	// Anthropic: the headers are read through the SDK's middleware
	asrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("anthropic-ratelimit-output-tokens-limit", "8000")
		w.Header().Set("anthropic-ratelimit-output-tokens-remaining", "7000")
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"m\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"x\",\"content\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}\n\n")
		fmt.Fprint(w, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
	}))
	defer asrv.Close()
	a := newAnthropic(config.Provider{ID: "rec-anthropic", Type: "anthropic", BaseURL: asrv.URL}, "k")
	if _, err := a.Chat(context.Background(), Request{Model: "x", Messages: []Message{{Role: "user", Content: []Block{{Type: "text", Text: "hi"}}}}}, nil); err != nil {
		t.Fatal(err)
	}
	if l, ok := ReportedLimits("rec-anthropic"); !ok || l.Limits[0].Name != "output tokens" || *l.Limits[0].Remaining != 7000 {
		t.Fatalf("anthropic recorded: %+v %v", l, ok)
	}
}

func TestContextWindow(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/openrouter/models":
			fmt.Fprint(w, `{"data":[{"id":"a/b","context_length":200000},{"id":"c/d","top_provider":{"context_length":32768}}]}`)
		case "/vllm/models":
			fmt.Fprint(w, `{"data":[{"id":"qwen","max_model_len":32768}]}`)
		case "/openai/models":
			fmt.Fprint(w, `{"data":[{"id":"gpt-x","owned_by":"openai"}]}`)
		case "/v1/models/claude-x":
			fmt.Fprint(w, `{"id":"claude-x","type":"model","display_name":"X","max_input_tokens":1000000,"max_tokens":128000}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	r := NewRouter([]config.Provider{
		{ID: "or", Type: "openai", BaseURL: srv.URL + "/openrouter"},
		{ID: "vl", Type: "openai", BaseURL: srv.URL + "/vllm"},
		{ID: "oa", Type: "openai", BaseURL: srv.URL + "/openai"},
		{ID: "an", Type: "anthropic", BaseURL: srv.URL, APIKeyRef: "vault://k"},
		{ID: "down", Type: "openai", BaseURL: srv.URL + "/missing"},
		{ID: "cli", Type: "claude-code"},
	}, func(string) (string, error) { return "k", nil })
	for id, want := range map[string]int{"or/a/b": 200000, "or/c/d": 32768, "vl/qwen": 32768, "oa/gpt-x": 0, "oa/other": 0, "an/claude-x": 1000000, "cli/sonnet": 0} {
		got, err := r.ContextWindow(context.Background(), id)
		if err != nil || got != want {
			t.Errorf("%s: %d, %v; want %d", id, got, err, want)
		}
	}
	if n, err := r.ContextWindow(context.Background(), "down/m"); err == nil || n != 0 {
		t.Errorf("an API error must be an error, not a number: %d %v", n, err)
	}
}

func TestOpenRouterQuota(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/key" || r.Header.Get("Authorization") != "Bearer k" {
			http.NotFound(w, r)
			return
		}
		fmt.Fprint(w, `{"data":{"label":"x","limit":10,"limit_remaining":7.5,"limit_reset":"monthly","usage":2.5}}`)
	}))
	defer srv.Close()
	o := newOpenAI(config.Provider{ID: "orq", Type: "openai", BaseURL: srv.URL + "/api/v1"}, "k")
	q, err := openRouterQuota(context.Background(), o)
	if err != nil || len(q) != 1 || *q[0].Limit != 10 || *q[0].Remaining != 7.5 || *q[0].Used != 2.5 || q[0].Period != "monthly" {
		t.Fatalf("quota: %+v %v", q, err)
	}

	// providers that report nothing are listed with no limits
	r := NewRouter([]config.Provider{{ID: "quiet", Name: "Quiet", Type: "openai", BaseURL: "http://127.0.0.1:1"}}, nil)
	rep := r.Limits(context.Background())
	if len(rep) != 1 || rep[0].ID != "quiet" || len(rep[0].Limits) != 0 || rep[0].Observed != nil || rep[0].Error != "" {
		t.Fatalf("report: %+v", rep)
	}
	if !isOpenRouter("https://openrouter.ai/api/v1") || isOpenRouter("https://api.openai.com/v1") {
		t.Fatal("isOpenRouter")
	}
}
