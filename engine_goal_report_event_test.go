package mow_test

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"github.com/subosito/mow"
)

// Tools that return ErrAgentDone (e.g. goal_report) must not surface as
// tool.end Error — the CLI would print "✗ goal_report: agent: done".
func TestErrAgentDoneNotEmittedAsToolError(t *testing.T) {
	t.Setenv("MOW_HOME", t.TempDir())
	t.Setenv("OPENAI_API_KEY", "k")
	t.Setenv("OPENAI_MODEL", "m")

	var mu sync.Mutex
	var endErr string
	var sawEnd bool
	eng, err := mow.New(mow.Options{
		Workspace: t.TempDir(),
		NoSession: true,
		Tools:     []mow.Tool{doneTool{}},
		Chat: func(ctx context.Context, messages []mow.Message, tools []mow.ToolSpec) (mow.Message, error) {
			return mow.Message{Role: "assistant", ToolCalls: []mow.ToolCall{{
				ID: "t1", Type: "function",
				Function: mow.FunctionCall{Name: "finish_now", Arguments: `{}`},
			}}}, nil
		},
		OnEvent: func(ev mow.Event) {
			if ev.Type == mow.EventToolEnd && ev.Tool == "finish_now" {
				mu.Lock()
				sawEnd = true
				endErr = ev.Error
				mu.Unlock()
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := eng.Prompt(context.Background(), "finish"); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if !sawEnd {
		t.Fatal("missing tool.end for finish_now")
	}
	if endErr != "" {
		t.Fatalf("tool.end Error=%q want empty (ErrAgentDone is success)", endErr)
	}
}

type doneTool struct{}

func (doneTool) Name() string                { return "finish_now" }
func (doneTool) Description() string         { return "finish" }
func (doneTool) Parameters() json.RawMessage { return json.RawMessage(`{}`) }
func (doneTool) Exec(context.Context, json.RawMessage) (string, error) {
	return "recorded: done", mow.ErrAgentDone
}
