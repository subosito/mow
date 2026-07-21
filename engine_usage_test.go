package mow

import (
	"context"
	"testing"
)

func TestRunResultUsageThreading(t *testing.T) {
	calls := 0
	res, err := Run(t.Context(), "hi", Options{
		NoSession: true,
		Chat: func(ctx context.Context, messages []Message, tools []ToolSpec) (Message, error) {
			calls++
			if calls == 1 {
				// One tool round-trip so usage must accumulate across calls.
				return Message{Role: "assistant", ToolCalls: []ToolCall{{
					ID: "c1", Type: "function",
					Function: FunctionCall{Name: "glob", Arguments: `{"path":"."}`},
				}}, Usage: Usage{InputTokens: 10, OutputTokens: 3}}, nil
			}
			return Message{Role: "assistant", Content: "done",
				Usage: Usage{InputTokens: 20, OutputTokens: 7}}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Usage.InputTokens != 30 || res.Usage.OutputTokens != 10 {
		t.Fatalf("usage=%+v want {30 10}", res.Usage)
	}
}
