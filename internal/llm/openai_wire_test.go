package llm

import (
	"encoding/json"
	"testing"
)

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
