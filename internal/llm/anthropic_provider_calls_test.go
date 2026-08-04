package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Anthropic reports native_tools work as server_tool_use / web_search_tool_result
// content blocks plus a usage.server_tool_use request count — a different shape
// from the Responses *_call items. Both must land on ProviderCalls, and neither
// may become an executable ToolCall: the name is Anthropic's server tool, not
// one the agent has.
func TestAnthropicServerToolUseIsReportedNotExecuted(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"stop_reason":"end_turn",
			"content":[
			  {"type":"server_tool_use","id":"srv1","name":"web_search"},
			  {"type":"web_search_tool_result","id":"res1"},
			  {"type":"text","text":"cited answer"}
			],
			"usage":{"input_tokens":10,"output_tokens":4,
			         "server_tool_use":{"web_search_requests":3}}}`))
	}))
	defer srv.Close()

	c := &Client{
		Wire: "anthropic-messages", BaseURL: srv.URL, APIKey: "k", Model: "claude-sonnet-4",
		NativeTools: []map[string]any{{"type": "web_search_20250305", "name": "web_search"}},
	}
	msg, err := c.Chat(context.Background(), []Message{{Role: "user", Content: "q"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(msg.ToolCalls) != 0 {
		t.Fatalf("server tool became executable: %#v", msg.ToolCalls)
	}
	if len(msg.ProviderCalls) != 2 {
		t.Fatalf("want both server blocks reported, got %#v", msg.ProviderCalls)
	}
	if msg.Usage.ServerSideToolCalls != 3 {
		t.Fatalf("server_tool_use request count lost: %#v", msg.Usage)
	}
	if msg.Content != "cited answer" {
		t.Fatalf("answer text lost: %q", msg.Content)
	}
}

// A normal tool_use block must still become a real ToolCall — the new case
// must not swallow the agent's own tools.
func TestAnthropicToolUseStillExecutes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"stop_reason":"tool_use","content":[
			{"type":"tool_use","id":"t1","name":"read","input":{"path":"a.go"}}],
			"usage":{"input_tokens":1,"output_tokens":1}}`))
	}))
	defer srv.Close()

	c := &Client{Wire: "anthropic-messages", BaseURL: srv.URL, APIKey: "k", Model: "claude-sonnet-4"}
	msg, err := c.Chat(context.Background(), []Message{{Role: "user", Content: "q"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(msg.ToolCalls) != 1 || msg.ToolCalls[0].Function.Name != "read" {
		t.Fatalf("agent tool call lost: %#v", msg.ToolCalls)
	}
	if len(msg.ProviderCalls) != 0 {
		t.Fatalf("agent tool misreported as provider call: %#v", msg.ProviderCalls)
	}
	var args map[string]any
	_ = json.Unmarshal([]byte(msg.ToolCalls[0].Function.Arguments), &args)
	if args["path"] != "a.go" {
		t.Fatalf("arguments mangled: %s", msg.ToolCalls[0].Function.Arguments)
	}
}
