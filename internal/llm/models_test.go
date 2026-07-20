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
				{"id": "glm-5.2"},
				{"id": "gpt-test"},
				{"id": "glm-5.2"}, // dedupe
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
	if list[0].ID != "glm-5.2" || list[1].ID != "gpt-test" {
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
