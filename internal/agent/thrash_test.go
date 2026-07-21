package agent_test

import (
	"context"
	"encoding/json"
	"errors"
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

type namedTool struct {
	name string
	n    int
}

func (t *namedTool) Name() string                { return t.name }
func (t *namedTool) Description() string         { return t.name }
func (t *namedTool) Parameters() json.RawMessage { return json.RawMessage(`{}`) }
func (t *namedTool) Exec(context.Context, json.RawMessage) (string, error) {
	t.n++
	return "ok", nil
}

// Same bash command over and over is unproductive thrash → stop.
func TestUnproductiveBashRepeatStops(t *testing.T) {
	bash := &namedTool{name: "bash"}
	n := 0
	chat := func(ctx context.Context, messages []llm.Message, tools []llm.ToolSpec) (llm.Message, error) {
		n++
		return llm.Message{
			Role: "assistant",
			ToolCalls: []llm.ToolCall{{
				ID:   fmt.Sprintf("c%d", n),
				Type: "function",
				Function: llm.FunctionCall{
					Name:      "bash",
					Arguments: `{"command":"find . -name x"}`, // identical every turn
				},
			}},
		}, nil
	}
	_, err := agent.Run(context.Background(), chat, "hi", agent.Options{
		MaxTurns:         0,
		MaxParallelTools: 1,
		Tools:            []agent.Tool{bash},
	})
	if err == nil || !errors.Is(err, agent.ErrStuck) {
		t.Fatalf("err=%v want ErrStuck", err)
	}
	// Identical tool fingerprint stalls after 3 turns (before unproductive counter).
	// Two execs run; the third turn trips stall before Exec.
	if bash.n < 2 || bash.n > 4 {
		t.Fatalf("bash execs=%d want ~2–3", bash.n)
	}
}

// New files every turn is legitimate exploration — must not hit stuck quickly.
func TestProductiveReadsDoNotStuckEarly(t *testing.T) {
	rt := &readOnceTool{}
	n := 0
	chat := func(ctx context.Context, messages []llm.Message, tools []llm.ToolSpec) (llm.Message, error) {
		n++
		if n > 20 {
			return llm.Message{Role: "assistant", Content: "done"}, nil
		}
		return llm.Message{
			Role: "assistant",
			ToolCalls: []llm.ToolCall{{
				ID:   fmt.Sprintf("c%d", n),
				Type: "function",
				Function: llm.FunctionCall{
					Name:      "read",
					Arguments: fmt.Sprintf(`{"path":"file%d.go"}`, n), // always new
				},
			}},
		}, nil
	}
	res, err := agent.Run(context.Background(), chat, "hi", agent.Options{
		MaxTurns:         0,
		MaxParallelTools: 1,
		Tools:            []agent.Tool{rt},
	})
	if err != nil {
		t.Fatalf("productive exploration should not stuck: %v", err)
	}
	if res.Text != "done" {
		t.Fatalf("text=%q", res.Text)
	}
	if rt.n != 20 {
		t.Fatalf("reads=%d want 20", rt.n)
	}
}
