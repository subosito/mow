package agent_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/subosito/mow/internal/agent"
	"github.com/subosito/mow/internal/llm"
)

func TestUnlimitedRunsUntilDone(t *testing.T) {
	n := 0
	chat := func(ctx context.Context, messages []llm.Message, tools []llm.ToolSpec) (llm.Message, error) {
		n++
		if n > 60 {
			return llm.Message{Role: "assistant", Content: "done"}, nil
		}
		return llm.Message{
			Role: "assistant",
			ToolCalls: []llm.ToolCall{{
				ID:   fmt.Sprintf("c%d", n),
				Type: "function",
				Function: llm.FunctionCall{
					Name:      "echo",
					Arguments: fmt.Sprintf(`{"text":"%d"}`, n),
				},
			}},
		}, nil
	}
	res, err := agent.Run(context.Background(), chat, "hi", agent.Options{
		MaxTurns:         0, // unlimited
		MaxParallelTools: 1,
		Tools:            []agent.Tool{echoTool{}},
	})
	if err != nil {
		t.Fatalf("unlimited should not error: %v", err)
	}
	if res.Text != "done" || n != 61 {
		t.Fatalf("text=%q n=%d", res.Text, n)
	}
}
