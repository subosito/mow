package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/subosito/mow/internal/llm"
)

// hardFailTool returns a hard (loop-aborting) error.
type hardFailTool struct{ name string }

func (t hardFailTool) Name() string                { return t.name }
func (t hardFailTool) Description() string         { return "fails hard" }
func (t hardFailTool) Parameters() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (t hardFailTool) Exec(ctx context.Context, _ json.RawMessage) (string, error) {
	return "", context.Canceled
}

func TestRepairToolResultsPadsOrphans(t *testing.T) {
	calls := []llm.ToolCall{
		{ID: "a", Function: llm.FunctionCall{Name: "read"}},
		{ID: "b", Function: llm.FunctionCall{Name: "grep"}},
		{ID: "c", Function: llm.FunctionCall{Name: "glob"}},
	}
	got := repairToolResults(calls, []llm.Message{
		{Role: "tool", ToolCallID: "b", Name: "grep", Content: "ok"},
	}, errors.New("boom"))
	if len(got) != 3 {
		t.Fatalf("want 3 tool results, got %d", len(got))
	}
	seen := map[string]string{}
	for _, m := range got {
		if m.Role != "tool" {
			t.Fatalf("non-tool message in results: %+v", m)
		}
		seen[m.ToolCallID] = m.Content
	}
	for _, id := range []string{"a", "b", "c"} {
		if _, ok := seen[id]; !ok {
			t.Fatalf("missing result for tool_call_id %q", id)
		}
	}
	if seen["b"] != "ok" {
		t.Fatalf("existing result overwritten: %q", seen["b"])
	}
	if !strings.Contains(seen["a"], "boom") {
		t.Fatalf("orphan result should mention reason, got %q", seen["a"])
	}
}

func TestRepairToolResultsNoopWhenComplete(t *testing.T) {
	calls := []llm.ToolCall{{ID: "a", Function: llm.FunctionCall{Name: "read"}}}
	in := []llm.Message{{Role: "tool", ToolCallID: "a", Content: "ok"}}
	got := repairToolResults(calls, in, nil)
	if len(got) != 1 {
		t.Fatalf("want unchanged results, got %d", len(got))
	}
}

// A hard tool error must not leave the assistant tool_calls turn unanswered:
// every advertised tool_call needs a matching tool message or the history
// cannot be replayed to the provider on resume.
func TestRunHistoryHasNoOrphanToolCalls(t *testing.T) {
	calls := []llm.ToolCall{
		{ID: "1", Type: "function", Function: llm.FunctionCall{Name: "boom", Arguments: "{}"}},
		{ID: "2", Type: "function", Function: llm.FunctionCall{Name: "boom", Arguments: "{}"}},
		{ID: "3", Type: "function", Function: llm.FunctionCall{Name: "boom", Arguments: "{}"}},
	}
	chat := func(ctx context.Context, _ []llm.Message, _ []llm.ToolSpec) (llm.Message, error) {
		return llm.Message{Role: "assistant", ToolCalls: calls}, nil
	}
	res, err := Run(context.Background(), chat, "go", Options{
		Tools:            []Tool{hardFailTool{name: "boom"}},
		MaxTurns:         3,
		MaxParallelTools: 4,
	})
	if err == nil {
		t.Fatal("want hard error from tool")
	}
	assertNoOrphanToolCalls(t, res.Messages)
}

func assertNoOrphanToolCalls(t *testing.T, msgs []llm.Message) {
	t.Helper()
	answered := map[string]bool{}
	for _, m := range msgs {
		if m.Role == "tool" {
			answered[m.ToolCallID] = true
		}
	}
	for _, m := range msgs {
		for _, tc := range m.ToolCalls {
			if !answered[tc.ID] {
				t.Fatalf("tool_call %q (%s) has no tool result; history is unreplayable",
					tc.ID, tc.Function.Name)
			}
		}
	}
}
