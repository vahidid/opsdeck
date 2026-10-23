package llm

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"opsdeck/internal/config"
)

func TestFetchModels(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models": // Anthropic, two pages
			if r.Header.Get("x-api-key") != "k-ant" {
				w.WriteHeader(401)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			if r.URL.Query().Get("after_id") == "" {
				fmt.Fprint(w, `{"data":[{"type":"model","id":"claude-opus-5-5","display_name":"Claude Opus 5.5"}],"has_more":true,"first_id":"claude-opus-5-5","last_id":"claude-opus-5-5"}`)
				return
			}
			fmt.Fprint(w, `{"data":[{"type":"model","id":"claude-haiku-5-5","display_name":"Claude Haiku 5.5"}],"has_more":false,"first_id":"claude-haiku-5-5","last_id":"claude-haiku-5-5"}`)
		case "/oa/models":
			if r.Header.Get("Authorization") != "Bearer k-oa" {
				w.WriteHeader(401)
				fmt.Fprint(w, `{"error":"bad key"}`)
				return
			}
			fmt.Fprint(w, `{"object":"list","data":[{"id":"b-model","owned_by":"acme"},{"id":"a-model","name":"A Model","description":"the first"}]}`)
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()

	got, err := FetchModels(context.Background(), config.Provider{Type: "anthropic", BaseURL: srv.URL}, "k-ant")
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(got) != "[{claude-opus-5-5 Claude Opus 5.5 } {claude-haiku-5-5 Claude Haiku 5.5 }]" {
		t.Errorf("anthropic: %v", got)
	}

	got, err = FetchModels(context.Background(), config.Provider{Type: "openai", BaseURL: srv.URL + "/oa/"}, "k-oa")
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(got) != "[{a-model A Model the first} {b-model b-model by acme}]" {
		t.Errorf("openai: %v", got)
	}
	if _, err := FetchModels(context.Background(), config.Provider{Type: "openai", BaseURL: srv.URL + "/oa"}, "wrong"); err == nil {
		t.Error("a rejected key should fail")
	}
}

func TestListModelsNames(t *testing.T) {
	m := ListModels([]config.Provider{{ID: "cc", Name: "Claude Code", Models: []string{"default", "opus"}, ModelNames: map[string]string{"opus": "Opus 5.5"}}})
	if m[0].Label != "Claude Code · default" || m[1].Label != "Claude Code · Opus 5.5" || m[1].Name != "Opus 5.5" || m[1].ID != "cc/opus" {
		t.Errorf("%+v", m)
	}
}
