package engine_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/subosito/mow"
)

type eventErrorTool struct{ err error }

func (eventErrorTool) Name() string                { return "error_probe" }
func (eventErrorTool) Description() string         { return "Test tool error reporting" }
func (eventErrorTool) Parameters() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (tool eventErrorTool) Exec(context.Context, json.RawMessage) (string, error) {
	return "", tool.err
}

func TestToolErrorsReachHooksAndEvents(t *testing.T) {
	for _, toolErr := range []error{errors.New("tool failed"), context.DeadlineExceeded} {
		t.Run(toolErr.Error(), func(t *testing.T) {
			var hookErr error
			var end mow.Event
			calls := 0
			eng, err := mow.New(mow.Options{
				NoSession: true,
				Tools:     []mow.Tool{eventErrorTool{err: toolErr}},
				Chat: func(_ context.Context, messages []mow.Message, _ []mow.ToolSpec) (mow.Message, error) {
					calls++
					if calls == 1 {
						return mow.Message{Role: "assistant", ToolCalls: []mow.ToolCall{{
							ID: "call1", Type: "function", Function: mow.FunctionCall{Name: "error_probe", Arguments: `{}`},
						}}}, nil
					}
					last := messages[len(messages)-1]
					if last.Role != "tool" || last.Content != "error: "+toolErr.Error() {
						t.Errorf("tool response = %+v", last)
					}
					return mow.Message{Role: "assistant", Content: "recovered"}, nil
				},
				Hooks: mow.Hooks{OnPostTool: []mow.PostToolFunc{
					func(_ context.Context, ev mow.PostToolEvent) (mow.PostToolDecision, error) {
						hookErr = ev.ExecErr
						return mow.PostToolDecision{}, nil
					},
				}},
				OnEvent: func(ev mow.Event) {
					if ev.Type == mow.EventToolEnd {
						end = ev
					}
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = eng.Close() })
			res, err := eng.Prompt(t.Context(), "run tool")
			if err != nil || res.Text != "recovered" {
				t.Fatalf("run should recover: %+v, err=%v", res, err)
			}
			if !errors.Is(hookErr, toolErr) {
				t.Fatalf("hook error = %v, want %v", hookErr, toolErr)
			}
			if end.Error != toolErr.Error() || end.Denied {
				t.Fatalf("tool end = %+v", end)
			}
		})
	}
}
