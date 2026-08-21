package focus_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/subosito/mow/internal/agent"
	"github.com/subosito/mow/internal/llm"

	"github.com/subosito/mow/ext/focus"
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
	opt := agent.Options{
		MaxTurns:         10,
		MaxParallelTools: 1,
		Tools:            []agent.Tool{rt},
	}
	focus.InstallForTest(&opt, focus.Config{})
	res, err := agent.Run(context.Background(), chat, "hi", opt)
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

type editOnceTool struct {
	n int
}

func (t *editOnceTool) Name() string        { return "edit" }
func (t *editOnceTool) Description() string { return "edit" }
func (t *editOnceTool) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}}}`)
}
func (t *editOnceTool) Exec(_ context.Context, args json.RawMessage) (string, error) {
	t.n++
	var a struct {
		Path string `json:"path"`
	}
	_ = json.Unmarshal(args, &a)
	return "EDITED:" + a.Path, nil
}

func TestRereadAllowedAfterEdit(t *testing.T) {
	rt := &readOnceTool{}
	et := &editOnceTool{}
	n := 0
	seq := []struct{ name, args string }{
		{"read", `{"path":"internal/port/port.go"}`},
		{"read", `{"path":"internal/port/port.go"}`},
		{"edit", `{"path":"internal/port/port.go"}`},
		{"read", `{"path":"internal/port/port.go"}`},
		{"read", `{"path":"internal/port/port.go"}`},
	}
	chat := func(ctx context.Context, messages []llm.Message, tools []llm.ToolSpec) (llm.Message, error) {
		n++
		if n > len(seq) {
			return llm.Message{Role: "assistant", Content: "done"}, nil
		}
		c := seq[n-1]
		return llm.Message{
			Role: "assistant",
			ToolCalls: []llm.ToolCall{{
				ID:       fmt.Sprintf("c%d", n),
				Type:     "function",
				Function: llm.FunctionCall{Name: c.name, Arguments: c.args},
			}},
		}, nil
	}
	opt := agent.Options{
		MaxTurns:         10,
		MaxParallelTools: 1,
		Tools:            []agent.Tool{rt, et},
	}
	focus.InstallForTest(&opt, focus.Config{})
	res, err := agent.Run(context.Background(), chat, "hi", opt)
	if err != nil {
		t.Fatal(err)
	}
	if rt.n != 2 {
		t.Fatalf("read Exec count=%d want 2 (second read stubbed; post-edit read allowed)", rt.n)
	}
	if et.n != 1 {
		t.Fatalf("edit Exec count=%d want 1", et.n)
	}
	var toolOuts []string
	for _, m := range res.Messages {
		if m.Role == "tool" {
			toolOuts = append(toolOuts, m.Content)
		}
	}
	if len(toolOuts) != 5 {
		t.Fatalf("tool msgs=%d want 5: %q", len(toolOuts), toolOuts)
	}
	if !strings.HasPrefix(toolOuts[0], "CONTENT:") {
		t.Fatalf("first read=%q", toolOuts[0])
	}
	if !strings.Contains(toolOuts[1], "already read") {
		t.Fatalf("second read=%q want already-read stub", toolOuts[1])
	}
	if !strings.HasPrefix(toolOuts[2], "EDITED:") {
		t.Fatalf("edit=%q", toolOuts[2])
	}
	if !strings.HasPrefix(toolOuts[3], "CONTENT:") {
		t.Fatalf("post-edit read=%q want live content", toolOuts[3])
	}
	if !strings.Contains(toolOuts[4], "already read") {
		t.Fatalf("second post-edit read=%q want already-read stub", toolOuts[4])
	}
}

// Unlimited MaxTurns must not hit a silent safety cap — loop until the model finishes.
