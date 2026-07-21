package agent_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/subosito/mow/internal/agent"
	"github.com/subosito/mow/internal/llm"
)

type readOnceTool struct {
	n int
}

func (t *readOnceTool) Name() string        { return "read" }
func (t *readOnceTool) Description() string { return "read" }
func (t *readOnceTool) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}}}`)
}
func (t *readOnceTool) Exec(_ context.Context, args json.RawMessage) (string, error) {
	t.n++
	var a struct {
		Path string `json:"path"`
	}
	_ = json.Unmarshal(args, &a)
	return "CONTENT:" + a.Path, nil
}

func TestRereadShortCircuit(t *testing.T) {
	rt := &readOnceTool{}
	n := 0
	chat := func(ctx context.Context, messages []llm.Message, tools []llm.ToolSpec) (llm.Message, error) {
		n++
		if n > 2 {
			return llm.Message{Role: "assistant", Content: "done"}, nil
		}
		return llm.Message{
			Role: "assistant",
			ToolCalls: []llm.ToolCall{{
				ID:   fmt.Sprintf("c%d", n),
				Type: "function",
				Function: llm.FunctionCall{
					Name:      "read",
					Arguments: `{"path":"internal/port/port.go"}`,
				},
			}},
		}, nil
	}
	res, err := agent.Run(context.Background(), chat, "hi", agent.Options{
		MaxTurns:         10,
		MaxParallelTools: 1,
		Tools:            []agent.Tool{rt},
	})
	if err != nil {
		t.Fatal(err)
	}
	if rt.n != 1 {
		t.Fatalf("Exec count=%d want 1 (second read short-circuited)", rt.n)
	}
	var toolOuts []string
	for _, m := range res.Messages {
		if m.Role == "tool" {
			toolOuts = append(toolOuts, m.Content)
		}
	}
	if len(toolOuts) != 2 {
		t.Fatalf("tool msgs=%d", len(toolOuts))
	}
	if !strings.HasPrefix(toolOuts[0], "CONTENT:") {
		t.Fatalf("first=%q", toolOuts[0])
	}
	if !strings.Contains(toolOuts[1], "already read") {
		t.Fatalf("second=%q want already-read stub", toolOuts[1])
	}
}

// Unlimited MaxTurns must not hit a silent safety cap — loop until the model finishes.
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
