package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/subosito/mow/internal/llm"
)

type seqTool struct{ ran *bool }

func (seqTool) Name() string                { return "ask_user" }
func (seqTool) Description() string         { return "" }
func (seqTool) Parameters() json.RawMessage { return nil }
func (t seqTool) Exec(context.Context, json.RawMessage) (string, error) {
	*t.ran = true
	return "", nil
}
func (seqTool) Sequential() bool { return true }

type normalTool struct{ ran *bool }

func (normalTool) Name() string                { return "write" }
func (normalTool) Description() string         { return "" }
func (normalTool) Parameters() json.RawMessage { return nil }
func (t normalTool) Exec(context.Context, json.RawMessage) (string, error) {
	*t.ran = true
	return "", nil
}
func TestSequentialToolRefusesEntireMixedBatch(t *testing.T) {
	a, b := false, false
	calls := []llm.ToolCall{{ID: "q", Function: llm.FunctionCall{Name: "ask_user", Arguments: `{}`}}, {ID: "w", Function: llm.FunctionCall{Name: "write", Arguments: `{}`}}}
	out, err := runToolBatch(t.Context(), calls, map[string]Tool{"ask_user": seqTool{&a}, "write": normalTool{&b}}, Options{})
	if err != nil || a || b || len(out) != 2 {
		t.Fatalf("out=%+v err=%v ran=%v/%v", out, err, a, b)
	}
}
