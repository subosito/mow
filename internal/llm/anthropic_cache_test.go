package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func captureAnthropic(t *testing.T, cache bool, reply string) map[string]any {
	t.Helper()
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(reply))
	}))
	t.Cleanup(srv.Close)
	c := &Client{Wire: WireAnthropicMsg, BaseURL: srv.URL, APIKey: "k", Model: "m", HTTP: srv.Client(), PromptCache: cache}
	tools := []ToolSpec{{Type: "function", Function: ToolSpecFunction{Name: "read"}}}
	if _, err := c.Chat(context.Background(), []Message{
		{Role: "system", Content: "big system prompt"},
		{Role: "user", Content: "hello"},
	}, tools); err != nil {
		t.Fatalf("chat: %v", err)
	}
	return body
}

func TestAnthropicPromptCacheMarkers(t *testing.T) {
	reply := `{"content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn"}`

	// Caching on: system is a cacheable block array, last tool and last message
	// carry cache_control.
	on := captureAnthropic(t, true, reply)
	sys, ok := on["system"].([]any)
	if !ok || len(sys) == 0 {
		t.Fatalf("system should be a block array when caching: %T", on["system"])
	}
	if _, has := sys[0].(map[string]any)["cache_control"]; !has {
		t.Fatalf("system block missing cache_control: %v", sys[0])
	}
	tools, _ := on["tools"].([]any)
	if len(tools) == 0 || tools[len(tools)-1].(map[string]any)["cache_control"] == nil {
		t.Fatalf("last tool missing cache_control: %v", on["tools"])
	}
	msgs, _ := on["messages"].([]any)
	last := msgs[len(msgs)-1].(map[string]any)
	blocks, ok := last["content"].([]any)
	if !ok || blocks[len(blocks)-1].(map[string]any)["cache_control"] == nil {
		t.Fatalf("last message missing cache_control: %v", last["content"])
	}

	// Caching off: system is a plain string, no cache markers.
	off := captureAnthropic(t, false, reply)
	if _, isStr := off["system"].(string); !isStr {
		t.Fatalf("system should be a plain string without caching: %T", off["system"])
	}
	tools2, _ := off["tools"].([]any)
	if tools2[len(tools2)-1].(map[string]any)["cache_control"] != nil {
		t.Fatal("tools should not carry cache_control when caching off")
	}
}

func TestAnthropicCacheTokensSummed(t *testing.T) {
	reply := `{"content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn",` +
		`"usage":{"input_tokens":10,"output_tokens":5,"cache_creation_input_tokens":100,"cache_read_input_tokens":900}}`
	var _ = reply
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(reply))
	}))
	t.Cleanup(srv.Close)
	c := &Client{Wire: WireAnthropicMsg, BaseURL: srv.URL, APIKey: "k", Model: "m", HTTP: srv.Client(), PromptCache: true}
	msg, err := c.Chat(context.Background(), []Message{{Role: "user", Content: "x"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	// 10 + 100 + 900 = 1010 total input.
	if msg.Usage.InputTokens != 1010 {
		t.Fatalf("input tokens=%d want 1010 (cached + non-cached summed)", msg.Usage.InputTokens)
	}
	if msg.Usage.OutputTokens != 5 {
		t.Fatalf("output tokens=%d want 5", msg.Usage.OutputTokens)
	}
}
