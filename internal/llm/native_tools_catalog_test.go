package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Native tool capability belongs to the model, and the gateway already
// publishes it on /v1/models. Taking it from there means one place describes
// the fleet instead of every client repeating the same list — and a client
// cannot declare a tool the model does not actually have.
func TestNativeToolsFromCatalog(t *testing.T) {
	var sentTools []any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/models" {
			_, _ = w.Write([]byte(`{"data":[
				{"id":"searcher","wire":"openai-responses","native_tools":[{"type":"web_search"}]},
				{"id":"plain","wire":"openai-responses"}]}`))
			return
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		sentTools, _ = body["tools"].([]any)
		_, _ = w.Write([]byte(`{"status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"ok"}]}]}`))
	}))
	defer srv.Close()

	base := &Client{Wire: WireOpenAIResponses, BaseURL: srv.URL, APIKey: "k", Model: "searcher"}
	if _, err := base.ListModels(context.Background()); err != nil {
		t.Fatal(err)
	}

	t.Run("catalog model declares its tool", func(t *testing.T) {
		sentTools = nil
		c := *base
		c.Model = "searcher"
		if _, err := c.Chat(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil); err != nil {
			t.Fatal(err)
		}
		if len(sentTools) != 1 {
			t.Fatalf("catalog tool not declared: %#v", sentTools)
		}
		m, _ := sentTools[0].(map[string]any)
		if m["type"] != "web_search" {
			t.Fatalf("wrong tool: %#v", sentTools[0])
		}
	})

	t.Run("model without the capability declares nothing", func(t *testing.T) {
		sentTools = nil
		c := *base
		c.Model = "plain"
		if _, err := c.Chat(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil); err != nil {
			t.Fatal(err)
		}
		if len(sentTools) != 0 {
			t.Fatalf("declared a tool the model has not got: %#v", sentTools)
		}
	})

	t.Run("local config overrides the catalog", func(t *testing.T) {
		sentTools = nil
		c := *base
		c.Model = "searcher"
		c.NativeTools = []map[string]any{{"type": "x_search"}}
		if _, err := c.Chat(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil); err != nil {
			t.Fatal(err)
		}
		if len(sentTools) != 1 {
			t.Fatalf("override lost: %#v", sentTools)
		}
		m, _ := sentTools[0].(map[string]any)
		if m["type"] != "x_search" {
			t.Fatalf("config did not win over catalog: %#v", sentTools[0])
		}
	})
}
