package engine_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/subosito/mow"
)

func TestAskUserIsPromptScopedAndReturnsAnswers(t *testing.T) {
	calls := 0
	eng, err := mow.New(mow.Options{Workspace: t.TempDir(), NoSession: true, Chat: func(_ context.Context, m []mow.Message, tools []mow.ToolSpec) (mow.Message, error) {
		calls++
		has := false
		for _, s := range tools {
			if s.Function.Name == "ask_user" {
				has = true
			}
		}
		if calls == 1 {
			if !has {
				t.Fatal("ask_user missing")
			}
			return mow.Message{Role: "assistant", ToolCalls: []mow.ToolCall{{ID: "q1", Type: "function", Function: mow.FunctionCall{Name: "ask_user", Arguments: `{"questions":[{"id":"pkg","prompt":"Choose","options":[{"id":"a","label":"A"},{"id":"b","label":"B"}]}]}`}}}}, nil
		}
		if !strings.Contains(m[len(m)-1].Content, `"selected":["b"]`) {
			t.Fatalf("answer=%q", m[len(m)-1].Content)
		}
		return mow.Message{Role: "assistant", Content: "done"}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()
	result, err := eng.PromptWith(t.Context(), "choose", mow.PromptOpts{AskUser: func(_ context.Context, q []mow.AskUserQuestion) ([]mow.AskUserAnswer, error) {
		return []mow.AskUserAnswer{{ID: q[0].ID, Selected: []string{"b"}}}, nil
	}})
	if err != nil || result.Text != "done" {
		t.Fatalf("%+v %v", result, err)
	}
}
func TestAskUserSequentialToolRefusesSiblings(t *testing.T) {
	ran := false
	calls := 0
	tool := testTool{name: "side", exec: func(context.Context, json.RawMessage) (string, error) { ran = true; return "bad", nil }}
	eng, err := mow.New(mow.Options{Workspace: t.TempDir(), NoSession: true, Tools: []mow.Tool{tool}, Chat: func(_ context.Context, m []mow.Message, _ []mow.ToolSpec) (mow.Message, error) {
		calls++
		if calls == 1 {
			return mow.Message{Role: "assistant", ToolCalls: []mow.ToolCall{{ID: "q", Function: mow.FunctionCall{Name: "ask_user", Arguments: `{"questions":[{"id":"x","prompt":"X"}]}`}}, {ID: "s", Function: mow.FunctionCall{Name: "side", Arguments: `{}`}}}}, nil
		}
		if !strings.Contains(m[len(m)-1].Content, "no sibling tools were executed") {
			t.Fatalf("results=%+v", m)
		}
		return mow.Message{Role: "assistant", Content: "stopped"}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()
	_, err = eng.PromptWith(t.Context(), "x", mow.PromptOpts{AskUser: func(context.Context, []mow.AskUserQuestion) ([]mow.AskUserAnswer, error) {
		t.Fatal("callback must not run")
		return nil, nil
	}})
	if err != nil || ran {
		t.Fatalf("err=%v ran=%v", err, ran)
	}
}

type testTool struct {
	name string
	exec func(context.Context, json.RawMessage) (string, error)
}

func (t testTool) Name() string                                              { return t.name }
func (t testTool) Description() string                                       { return t.name }
func (t testTool) Parameters() json.RawMessage                               { return json.RawMessage(`{"type":"object"}`) }
func (t testTool) Exec(c context.Context, a json.RawMessage) (string, error) { return t.exec(c, a) }
