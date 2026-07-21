package mow

import (
	"context"
	"encoding/json"
	"testing"
)

type fakeTool struct {
	name     string
	readOnly bool
	got      *string
}

func (t *fakeTool) Name() string { return t.name }

func (t *fakeTool) Description() string { return "fake" }

func (t *fakeTool) Parameters() json.RawMessage { return nil }

func (t *fakeTool) Exec(ctx context.Context, args json.RawMessage) (string, error) {
	if t.got != nil {
		*t.got = t.name
	}
	return "fake-result:" + t.name, nil
}

func (t *fakeTool) ReadOnly() bool { return t.readOnly }

func TestPerEngineTools(t *testing.T) {
	var called string
	chat := func(ctx context.Context, messages []Message, tools []ToolSpec) (Message, error) {
		// First call: invoke the custom tool; second: finish.
		for _, m := range messages {
			if m.Role == "tool" {
				return Message{Role: "assistant", Content: "done " + m.Content}, nil
			}
		}
		return Message{Role: "assistant", ToolCalls: []ToolCall{{
			ID: "c1", Type: "function",
			Function: FunctionCall{Name: "lookup", Arguments: `{}`},
		}}}, nil
	}
	res, err := Run(t.Context(), "go", Options{
		NoSession: true,
		Chat:      chat,
		Tools:     []Tool{&fakeTool{name: "lookup", readOnly: true, got: &called}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if called != "lookup" {
		t.Fatalf("per-engine tool not executed (called=%q, res=%q)", called, res.Text)
	}

	// Engine isolation: a second engine without the tool must not see it.
	eng2, err := New(Options{NoSession: true, Chat: chat})
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range eng2.tools {
		if tool.Name() == "lookup" {
			t.Fatal("per-engine tool leaked into another engine")
		}
	}

	// Builtin collision is rejected.
	if _, err := New(Options{NoSession: true, Chat: chat,
		Tools: []Tool{&fakeTool{name: "read"}}}); err == nil {
		t.Fatal("builtin-name collision should error")
	}
}
