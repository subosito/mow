package llm

import "testing"

func TestToAnthropicMessages(t *testing.T) {
	sys, msgs := toAnthropicMessages([]Message{
		{Role: "system", Content: "be nice"},
		{Role: "user", Content: "hi"},
		{Role: "assistant", Content: "hello", ToolCalls: []ToolCall{
			{ID: "1", Type: "function", Function: FunctionCall{Name: "read", Arguments: `{"path":"a"}`}},
		}},
		{Role: "tool", ToolCallID: "1", Content: "file"},
	})
	if sys != "be nice" {
		t.Fatalf("sys=%q", sys)
	}
	if len(msgs) < 3 {
		t.Fatalf("msgs=%d", len(msgs))
	}
}
