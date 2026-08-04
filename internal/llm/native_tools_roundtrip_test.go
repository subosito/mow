package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// End-to-end: config-shaped native tool -> outbound body -> provider runs it
// -> parsed back as an observable ProviderCall (never an executable ToolCall).
func TestNativeToolRoundTrip(t *testing.T) {
	var sentTools []any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		sentTools, _ = body["tools"].([]any)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"completed","output":[
			{"type":"web_search_call","id":"ws1","status":"completed"},
			{"type":"message","content":[{"type":"output_text","text":"cited answer"}]}],
			"usage":{"input_tokens":10,"output_tokens":5,"num_sources_used":3,"num_server_side_tool_calls":1}}`))
	}))
	defer srv.Close()

	c := &Client{
		Wire: "openai-responses", BaseURL: srv.URL, APIKey: "k", Model: "grok-4.5",
		NativeTools: []map[string]any{{"type": "web_search"}},
	}
	msg, err := c.Chat(context.Background(), []Message{{Role: "user", Content: "q"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(sentTools) != 1 {
		t.Fatalf("declaration did not reach provider: %#v", sentTools)
	}
	if len(msg.ProviderCalls) != 1 || msg.ProviderCalls[0].Type != "web_search_call" {
		t.Fatalf("provider call not reported: %#v", msg.ProviderCalls)
	}
	if len(msg.ToolCalls) != 0 {
		t.Fatalf("CRITICAL: would try to execute provider tool: %#v", msg.ToolCalls)
	}
	if msg.Usage.ServerSideToolCalls != 1 || msg.Usage.SourcesUsed != 3 {
		t.Fatalf("usage extras lost: %#v", msg.Usage)
	}
	if msg.Content != "cited answer" {
		t.Fatalf("answer text lost: %q", msg.Content)
	}
}
