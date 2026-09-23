package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/subosito/mow/internal/llm"
)

type postBatchTool struct{}

func (postBatchTool) Name() string                { return "delegate" }
func (postBatchTool) Description() string         { return "" }
func (postBatchTool) Parameters() json.RawMessage { return nil }
func (postBatchTool) Exec(context.Context, json.RawMessage) (string, error) {
	return "peer evidence", nil
}
func (postBatchTool) PostBatchContext() string { return "synthesize peers now" }
func TestPostBatchContextAppendedOnce(t *testing.T) {
	tool := postBatchTool{}
	calls := []llm.ToolCall{{ID: "a", Function: llm.FunctionCall{Name: "delegate", Arguments: `{}`}}, {ID: "b", Function: llm.FunctionCall{Name: "delegate", Arguments: `{}`}}}
	out, err := runToolBatch(t.Context(), calls, map[string]Tool{"delegate": tool}, Options{})
	if err != nil || len(out) != 2 || strings.Count(out[1].Content, "synthesize peers now") != 1 {
		t.Fatalf("out=%+v err=%v", out, err)
	}
}
