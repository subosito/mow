package mow_test

import (
	"context"
	"strings"
	"testing"

	"github.com/subosito/mow"
)

func TestPromptReadOnlyDeniesWrite(t *testing.T) {
	var sawDeny bool
	eng, err := mow.New(mow.Options{
		NoSession:  true,
		AllowWrite: true,
		Chat: func(ctx context.Context, messages []mow.Message, tools []mow.ToolSpec) (mow.Message, error) {
			// Ask model to call write — simulate by checking tools still listed but policy denies at exec via AllowTool.
			// Direct path: invoke through a forced tool loop is heavy; unit-test AllowTool via agent by tool call.
			return mow.Message{
				Role: "assistant",
				ToolCalls: []mow.ToolCall{{
					ID: "1", Type: "function",
					Function: mow.FunctionCall{Name: "write", Arguments: `{"path":"x","content":"y"}`},
				}},
			}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	// Second chat turn after tool result
	// Actually first call returns tool call, then loop executes write — ReadOnly should deny.
	// Need chat to eventually stop. Use counter.
	n := 0
	eng, err = mow.New(mow.Options{
		NoSession:  true,
		AllowWrite: true,
		Chat: func(ctx context.Context, messages []mow.Message, tools []mow.ToolSpec) (mow.Message, error) {
			n++
			if n == 1 {
				return mow.Message{
					Role: "assistant",
					ToolCalls: []mow.ToolCall{{
						ID: "1", Type: "function",
						Function: mow.FunctionCall{Name: "write", Arguments: `{"path":"x.txt","content":"y"}`},
					}},
				}, nil
			}
			// After tool result in history
			for _, m := range messages {
				if m.Role == "tool" && strings.Contains(m.Content, "denied") {
					sawDeny = true
				}
			}
			return mow.Message{Role: "assistant", Content: "ok"}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = eng.PromptWith(context.Background(), "write a file", mow.PromptOpts{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	if !sawDeny {
		t.Fatal("expected write denied in tool result")
	}
}
