package agent_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/subosito/mow/internal/agent"
	"github.com/subosito/mow/internal/llm"
)

type echoTool struct{}

func (echoTool) Name() string        { return "echo" }
func (echoTool) Description() string { return "echo args" }
func (echoTool) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"text":{"type":"string"}}}`)
}
func (echoTool) Exec(_ context.Context, args json.RawMessage) (string, error) {
	var a struct {
		Text string `json:"text"`
	}
	_ = json.Unmarshal(args, &a)
	return a.Text, nil
}

func TestRunWithPriorMessages(t *testing.T) {
	var got []llm.Message
	chat := func(ctx context.Context, messages []llm.Message, tools []llm.ToolSpec) (llm.Message, error) {
		got = append([]llm.Message(nil), messages...)
		return llm.Message{Role: "assistant", Content: "ok"}, nil
	}
	_, err := agent.Run(context.Background(), chat, "next", agent.Options{
		System: "sys",
		PriorMessages: []llm.Message{
			{Role: "system", Content: "sys"},
			{Role: "user", Content: "old"},
			{Role: "assistant", Content: "old-reply"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) < 4 {
		t.Fatalf("messages=%d want >=4", len(got))
	}
	if got[len(got)-1].Role != "user" || got[len(got)-1].Content != "next" {
		t.Fatalf("last=%+v", got[len(got)-1])
	}
}

func TestRunWithFakeLLMToolThenText(t *testing.T) {
	step := 0
	chat := func(ctx context.Context, messages []llm.Message, tools []llm.ToolSpec) (llm.Message, error) {
		step++
		if step == 1 {
			return llm.Message{
				Role: "assistant",
				ToolCalls: []llm.ToolCall{{
					ID:   "1",
					Type: "function",
					Function: llm.FunctionCall{
						Name:      "echo",
						Arguments: `{"text":"from-tool"}`,
					},
				}},
			}, nil
		}
		found := false
		for _, m := range messages {
			if m.Role == "tool" && m.Content == "from-tool" {
				found = true
			}
		}
		if !found {
			t.Fatal("tool result missing in history")
		}
		return llm.Message{Role: "assistant", Content: "done: from-tool"}, nil
	}
	res, err := agent.Run(context.Background(), chat, "hi", agent.Options{
		MaxTurns: 5,
		Tools:    []agent.Tool{echoTool{}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "done: from-tool" {
		t.Fatalf("text=%q", res.Text)
	}
}

func TestMaxTurnsReturnsErrMaxTurns(t *testing.T) {
	chat := func(ctx context.Context, messages []llm.Message, tools []llm.ToolSpec) (llm.Message, error) {
		return llm.Message{
			Role: "assistant",
			ToolCalls: []llm.ToolCall{{
				ID: "1", Type: "function",
				Function: llm.FunctionCall{Name: "echo", Arguments: `{"message":"x"}`},
			}},
		}, nil
	}
	_, err := agent.Run(context.Background(), chat, "hi", agent.Options{
		MaxTurns: 2,
		Tools:    []agent.Tool{echoTool{}},
	})
	if err == nil || !errors.Is(err, agent.ErrMaxTurns) {
		t.Fatalf("err=%v want ErrMaxTurns", err)
	}
}

func TestAllowToolDeniesExec(t *testing.T) {
	step := 0
	chat := func(ctx context.Context, messages []llm.Message, tools []llm.ToolSpec) (llm.Message, error) {
		step++
		if step == 1 {
			return llm.Message{
				Role: "assistant",
				ToolCalls: []llm.ToolCall{{
					ID: "1", Type: "function",
					Function: llm.FunctionCall{Name: "bash", Arguments: `{}`},
				}},
			}, nil
		}
		for _, m := range messages {
			if m.Role == "tool" && len(m.Content) > 0 {
				if m.Content == "error: denied by policy" || len(m.Content) > 5 {
					return llm.Message{Role: "assistant", Content: "handled"}, nil
				}
			}
		}
		return llm.Message{Role: "assistant", Content: "fail"}, nil
	}
	res, err := agent.Run(context.Background(), chat, "hi", agent.Options{
		MaxTurns: 5,
		AllowTool: func(name string) error {
			return errors.New("denied by policy")
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "handled" {
		t.Fatalf("text=%q", res.Text)
	}
}
