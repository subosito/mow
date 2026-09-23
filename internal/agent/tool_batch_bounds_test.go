package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/subosito/mow/internal/llm"
)

type countingTool struct{ runs int }

func (c *countingTool) Name() string                { return "read" }
func (c *countingTool) Description() string         { return "" }
func (c *countingTool) Parameters() json.RawMessage { return nil }
func (c *countingTool) Exec(context.Context, json.RawMessage) (string, error) {
	c.runs++
	return "ok", nil
}
func TestPathologicalAndMalformedToolBatchesDoNotExecute(t *testing.T) {
	tool := &countingTool{}
	calls := make([]llm.ToolCall, 65)
	for i := range calls {
		calls[i] = llm.ToolCall{ID: string(rune(1000 + i)), Function: llm.FunctionCall{Name: "read", Arguments: `{}`}}
	}
	out, err := runToolBatch(t.Context(), calls, map[string]Tool{"read": tool}, Options{})
	if err != nil || tool.runs != 0 || len(out) != 65 || !strings.Contains(out[0].Content, "maximum is 64") {
		t.Fatalf("runs=%d out=%d err=%v", tool.runs, len(out), err)
	}
	tool.runs = 0
	bad := []llm.ToolCall{{ID: "", Function: llm.FunctionCall{Name: "read", Arguments: `{}`}}}
	out, err = runToolBatch(t.Context(), bad, map[string]Tool{"read": tool}, Options{})
	if err != nil || tool.runs != 0 || len(out) != 1 || !strings.Contains(out[0].Content, "without id or function name") {
		t.Fatalf("bad=%+v", out)
	}
}
