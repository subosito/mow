package mow_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/subosito/mow"
	"github.com/subosito/mow/ext"
)

func TestEngineHooksUserPromptAndStop(t *testing.T) {
	ext.Reset()
	t.Cleanup(ext.Reset)

	var stopped string
	ext.RegisterUserPrompt(func(ctx context.Context, e ext.UserPromptEvent) (ext.UserPromptDecision, error) {
		return ext.UserPromptDecision{
			RewriteText:  true,
			Text:         "rewritten:" + e.Text,
			SystemAppend: "extra-sys",
		}, nil
	})
	ext.RegisterStop(func(ctx context.Context, e ext.StopEvent) {
		stopped = e.Text
	})
	ext.RegisterSessionStart(func(ctx context.Context, e ext.SessionStartEvent) (ext.SessionStartDecision, error) {
		return ext.SessionStartDecision{SystemAppend: "from-start"}, nil
	})

	var sawSys []string
	chat := func(ctx context.Context, messages []mow.Message, tools []mow.ToolSpec) (mow.Message, error) {
		for _, m := range messages {
			if m.Role == "system" {
				sawSys = append(sawSys, m.Content)
			}
			if m.Role == "user" && !strings.HasPrefix(m.Content, "rewritten:") {
				t.Fatalf("user text not rewritten: %q", m.Content)
			}
		}
		return mow.Message{Role: "assistant", Content: "done"}, nil
	}

	eng, err := mow.New(mow.Options{NoSession: true, Chat: chat})
	if err != nil {
		t.Fatal(err)
	}
	res, err := eng.Prompt(context.Background(), "hello")
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "done" {
		t.Fatalf("text=%q", res.Text)
	}
	if stopped != "done" {
		t.Fatalf("stop=%q", stopped)
	}
	foundStart, foundExtra := false, false
	for _, s := range sawSys {
		if strings.Contains(s, "from-start") {
			foundStart = true
		}
		if strings.Contains(s, "extra-sys") {
			foundExtra = true
		}
	}
	if !foundStart || !foundExtra {
		t.Fatalf("system hooks missing: start=%v extra=%v sys=%q", foundStart, foundExtra, sawSys)
	}
}

func TestEngineOptionsPreToolDeny(t *testing.T) {
	ext.Reset()
	t.Cleanup(ext.Reset)

	step := 0
	chat := func(ctx context.Context, messages []mow.Message, tools []mow.ToolSpec) (mow.Message, error) {
		step++
		if step == 1 {
			return mow.Message{
				Role: "assistant",
				ToolCalls: []mow.ToolCall{{
					ID: "1", Type: "function",
					Function: mow.FunctionCall{Name: "echo", Arguments: `{}`},
				}},
			}, nil
		}
		for _, m := range messages {
			if m.Role == "tool" && strings.Contains(m.Content, "nope") {
				return mow.Message{Role: "assistant", Content: "blocked-ok"}, nil
			}
		}
		return mow.Message{Role: "assistant", Content: "fail"}, nil
	}

	// Register a minimal echo tool so the loop has something to call.
	ext.RegisterTool(echoTool{})

	eng, err := mow.New(mow.Options{
		NoSession: true,
		Chat:      chat,
		Hooks: mow.Hooks{
			OnPreTool: []mow.PreToolFunc{
				func(ctx context.Context, e mow.PreToolEvent) (mow.PreToolDecision, error) {
					return mow.PreToolDecision{Deny: true, Message: "nope"}, nil
				},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	res, err := eng.Prompt(context.Background(), "hi")
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "blocked-ok" {
		t.Fatalf("text=%q", res.Text)
	}
}

type echoTool struct{}

func (echoTool) Name() string        { return "echo" }
func (echoTool) Description() string { return "echo" }
func (echoTool) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{}}`)
}
func (echoTool) Exec(_ context.Context, _ json.RawMessage) (string, error) {
	return "should-not-run", nil
}
