package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Some providers require thought_signature on functionCall parts when
// replaying tool_calls. Capture must survive history → toOpenAIMessages.
func TestToOpenAIMessagesPreservesThoughtSignature(t *testing.T) {
	in := []Message{
		{Role: "user", Content: "list files"},
		{Role: "assistant", ToolCalls: []ToolCall{{
			ID: "call_1", Type: "function",
			ThoughtSignature: "sig-opaque-abc",
			Function:         FunctionCall{Name: "glob", Arguments: `{"pattern":"**/*"}`},
		}}},
		{Role: "tool", ToolCallID: "call_1", Name: "glob", Content: "a.go"},
	}
	wire := toOpenAIMessages(in)
	raw, err := json.Marshal(ChatRequest{Model: "m", Messages: wire})
	if err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		Messages []struct {
			ToolCalls []struct {
				ThoughtSignature string `json:"thought_signature"`
			} `json:"tool_calls"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatal(err)
	}
	if len(parsed.Messages) < 2 || len(parsed.Messages[1].ToolCalls) != 1 {
		t.Fatalf("body=%s", raw)
	}
	if got := parsed.Messages[1].ToolCalls[0].ThoughtSignature; got != "sig-opaque-abc" {
		t.Fatalf("thought_signature=%q body=%s", got, raw)
	}
	// Empty signature must not appear (omitempty) for plain OpenAI payloads.
	plain := toOpenAIMessages([]Message{{
		Role: "assistant",
		ToolCalls: []ToolCall{{
			ID: "c", Type: "function",
			Function: FunctionCall{Name: "read", Arguments: `{}`},
		}},
	}})
	raw2, _ := json.Marshal(plain[0])
	if strings.Contains(string(raw2), "thought_signature") {
		t.Fatalf("empty signature should omit: %s", raw2)
	}
}

// Regression: OpenAI-compat gateways with untagged MessageContent (Text|Parts)
// reject assistant tool-call turns and empty tool results when content is
// omitted (Go omitempty) or null. Goals hit this mid multi-turn tool loop.
func TestToOpenAIMessagesAlwaysEmitsContentString(t *testing.T) {
	in := []Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "go"},
		{Role: "assistant", ToolCalls: []ToolCall{{
			ID: "c1", Type: "function",
			Function: FunctionCall{Name: "read", Arguments: `{"path":"a.go"}`},
		}}},
		{Role: "tool", ToolCallID: "c1", Name: "read", Content: ""},
		{Role: "assistant", ToolCalls: []ToolCall{{
			ID:       "c2", // type and args empty — stream partial
			Function: FunctionCall{Name: "glob", Arguments: ""},
		}}},
	}
	wire := toOpenAIMessages(in)
	raw, err := json.Marshal(ChatRequest{Model: "m", Messages: wire})
	if err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		Messages []map[string]json.RawMessage `json:"messages"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatal(err)
	}
	if len(parsed.Messages) != len(in) {
		t.Fatalf("len=%d want %d body=%s", len(parsed.Messages), len(in), raw)
	}
	for i, m := range parsed.Messages {
		c, ok := m["content"]
		if !ok {
			t.Fatalf("messages[%d] missing content key: %s", i, raw)
		}
		if string(c) == "null" {
			t.Fatalf("messages[%d] content is null: %s", i, raw)
		}
		var asString string
		if err := json.Unmarshal(c, &asString); err != nil {
			t.Fatalf("messages[%d] content not a string: %s err=%v", i, c, err)
		}
	}
	// Empty tool result still has content "".
	var toolContent string
	if err := json.Unmarshal(parsed.Messages[3]["content"], &toolContent); err != nil {
		t.Fatal(err)
	}
	if toolContent != "" {
		t.Fatalf("tool content=%q want empty string", toolContent)
	}
	// Normalized type + empty arguments → {}.
	asst := wire[4]
	if len(asst.ToolCalls) != 1 {
		t.Fatalf("tool_calls=%d", len(asst.ToolCalls))
	}
	if asst.ToolCalls[0].Type != "function" {
		t.Fatalf("type=%q", asst.ToolCalls[0].Type)
	}
	if asst.ToolCalls[0].Function.Arguments != "{}" {
		t.Fatalf("arguments=%q", asst.ToolCalls[0].Function.Arguments)
	}
}

func TestChatOpenAIParsesThoughtSignature(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"choices":[{
				"message":{
					"role":"assistant",
					"content":"",
					"tool_calls":[{
						"id":"call_1",
						"type":"function",
						"thought_signature":"sig-ns",
						"function":{"name":"glob","arguments":"{\"pattern\":\"*\"}"}
					}]
				},
				"finish_reason":"tool_calls"
			}],
			"usage":{"prompt_tokens":1,"completion_tokens":2}
		}`))
	}))
	defer srv.Close()

	c := &Client{BaseURL: srv.URL + "/v1", APIKey: "k", Model: "m", HTTP: srv.Client()}
	msg, err := c.Chat(context.Background(), []Message{{Role: "user", Content: "x"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(msg.ToolCalls) != 1 || msg.ToolCalls[0].ThoughtSignature != "sig-ns" {
		t.Fatalf("msg=%+v", msg)
	}
}
