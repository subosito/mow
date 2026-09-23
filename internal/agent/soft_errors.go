package agent

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/subosito/mow/internal/llm"
)

type softErrorTool struct{ name, message string }

func (t softErrorTool) Name() string              { return t.name }
func (softErrorTool) Description() string         { return "" }
func (softErrorTool) Parameters() json.RawMessage { return nil }
func (t softErrorTool) Exec(context.Context, json.RawMessage) (string, error) {
	return "", fmt.Errorf("%s", t.message)
}

func runPolicyDenied(ctx context.Context, tool Tool, tc llm.ToolCall, opt Options, policyErr error) toolSlot {
	hooks := opt.Hooks
	hooks.PreTool = append([]PreToolFunc{func(context.Context, PreToolEvent) (PreToolDecision, error) {
		return PreToolDecision{Deny: true, Message: policyErr.Error()}, nil
	}}, hooks.PreTool...)
	args := json.RawMessage(tc.Function.Arguments)
	if len(args) == 0 {
		args = json.RawMessage(`{}`)
	}
	out, err := runTool(ctx, tool, tc.Function.Name, tc.ID, args, hooks, func(s string) string { return TruncateToolResult(s, toolResultLimit(opt)) })
	if err != nil {
		return toolSlot{hard: err}
	}
	return toolSlot{ok: true, msg: llm.Message{Role: "tool", ToolCallID: tc.ID, Name: tc.Function.Name, Content: out}}
}

func runUnknownTool(ctx context.Context, tc llm.ToolCall, opt Options) toolSlot {
	args := json.RawMessage(tc.Function.Arguments)
	if len(args) == 0 {
		args = json.RawMessage(`{}`)
	}
	tool := softErrorTool{name: tc.Function.Name, message: fmt.Sprintf("unknown tool %q", tc.Function.Name)}
	out, err := runTool(ctx, tool, tc.Function.Name, tc.ID, args, opt.Hooks, func(s string) string { return TruncateToolResult(s, toolResultLimit(opt)) })
	if err != nil {
		return toolSlot{hard: err}
	}
	return toolSlot{ok: true, msg: llm.Message{Role: "tool", ToolCallID: tc.ID, Name: tc.Function.Name, Content: out}}
}
