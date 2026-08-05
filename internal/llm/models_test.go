package llm_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/subosito/mow/internal/llm"
)

func TestListModels(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			t.Fatalf("path=%s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer k" {
			t.Fatalf("auth=%q", r.Header.Get("Authorization"))
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]string{
				{"id": "deepseek-chat"},
				{"id": "gpt-test"},
				{"id": "deepseek-chat"}, // dedupe
			},
		})
	}))
	defer srv.Close()

	c := &llm.Client{BaseURL: srv.URL + "/v1", APIKey: "k"}
	list, err := c.ListModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("len=%d %#v", len(list), list)
	}
	if list[0].ID != "deepseek-chat" || list[1].ID != "gpt-test" {
		t.Fatalf("sorted=%v", list)
	}
}

func TestListModelsWorksForAnthropicWire(t *testing.T) {
	// Same OpenAI-shaped /models endpoint regardless of chat wire.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			t.Fatalf("path=%s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{
				{"id": "claude-x", "wire": "anthropic-messages", "wires": []string{"anthropic-messages"}},
			},
		})
	}))
	defer srv.Close()
	c := &llm.Client{Wire: llm.WireAnthropicMsg, BaseURL: srv.URL + "/v1", APIKey: "k"}
	list, err := c.ListModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].ID != "claude-x" || list[0].Wire != "anthropic-messages" {
		t.Fatalf("%+v", list)
	}
}

func TestListModelsParsesFacet(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{
				{"id": "model-x", "wire": "openai-responses", "facet": "chat"},
				{"id": "model-alias", "wires": []string{"openai-responses"}, "facet": "chat", "models": []string{"model-x"}},
				{"id": "model-x:search", "wire": "openai-responses", "facet": "search"},
				{"id": "reviewers", "object": "model_group", "models": []string{"model-x"}},
			},
		})
	}))
	defer srv.Close()
	c := &llm.Client{BaseURL: srv.URL + "/v1", APIKey: "k"}
	list, err := c.ListModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]llm.ModelInfo{}
	for _, m := range list {
		byID[m.ID] = m
	}
	if byID["model-x"].Facet != "chat" {
		t.Fatalf("chat facet: %+v", byID["model-x"])
	}
	if got := byID["model-alias"].Wire; got != llm.WireOpenAIResponses {
		t.Fatalf("alias preferred wire=%q want %q", got, llm.WireOpenAIResponses)
	}
	// Non-chat facets and discovery-only model groups are filtered out of
	// the list and catalog so pickers never offer a non-callable model.
	if _, ok := byID["model-x:search"]; ok {
		t.Fatalf("search facet should be filtered: %+v", byID["model-x:search"])
	}
	if _, ok := byID["reviewers"]; ok {
		t.Fatalf("model_group should be filtered: %+v", byID["reviewers"])
	}
}

func TestListModelsParsesContextAndPricing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{
				{
					"id":                "gpt-5-mini",
					"context_window":    1100000,
					"max_output_tokens": 0,
					"pricing": map[string]any{
						"currency":            "USD",
						"input_per_mtok":      2.5,
						"output_per_mtok":     15,
						"cache_read_per_mtok": 0.25,
					},
				},
			},
		})
	}))
	defer srv.Close()
	c := &llm.Client{BaseURL: srv.URL + "/v1", APIKey: "k"}
	list, err := c.ListModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("len=%d", len(list))
	}
	m := list[0]
	if m.ContextWindow != 1_100_000 || m.Pricing.InputPerMTok != 2.5 || m.Pricing.OutputPerMTok != 15 {
		t.Fatalf("parsed %+v", m)
	}
	info, ok := c.CatalogEntry("gpt-5-mini")
	if !ok || info.ContextWindow != 1_100_000 {
		t.Fatalf("catalog %+v ok=%v", info, ok)
	}
}
