package agent

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/subosito/mow/internal/llm"
)

type cappedTool struct {
	mu           sync.Mutex
	active, peak int
}

func (c *cappedTool) Name() string                { return "delegate" }
func (c *cappedTool) Description() string         { return "" }
func (c *cappedTool) Parameters() json.RawMessage { return nil }
func (c *cappedTool) MaxParallel() int            { return 2 }
func (c *cappedTool) Exec(context.Context, json.RawMessage) (string, error) {
	c.mu.Lock()
	c.active++
	c.peak = max(c.peak, c.active)
	c.mu.Unlock()
	time.Sleep(20 * time.Millisecond)
	c.mu.Lock()
	c.active--
	c.mu.Unlock()
	return "ok", nil
}
func TestPerToolParallelCap(t *testing.T) {
	tool := &cappedTool{}
	var calls []llm.ToolCall
	for i := range 6 {
		calls = append(calls, llm.ToolCall{ID: string(rune('a' + i)), Function: llm.FunctionCall{Name: "delegate", Arguments: `{}`}})
	}
	out, err := runToolBatch(t.Context(), calls, map[string]Tool{"delegate": tool}, Options{MaxParallelTools: 8})
	if err != nil || len(out) != 6 || tool.peak > 2 {
		t.Fatalf("out=%d err=%v peak=%d", len(out), err, tool.peak)
	}
}
