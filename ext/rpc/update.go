package rpc

import (
	"strings"

	"github.com/subosito/mow"
)

// typedUpdate projects an Engine event onto a small host-facing update.
// Unknown types return nil so the raw `event` notification stays the
// source of truth. Feature-detect via capabilities.features.typed_updates.
func typedUpdate(ev mow.Event) map[string]any {
	switch ev.Type {
	case mow.EventType("loop.token"):
		if ev.Delta == "" {
			return nil
		}
		return map[string]any{"kind": "token", "delta": ev.Delta, "run_id": ev.RunID}
	case mow.EventType("loop.reasoning"):
		if ev.Delta == "" {
			return nil
		}
		return map[string]any{"kind": "thought", "delta": ev.Delta, "run_id": ev.RunID}
	case mow.EventToolStart:
		return map[string]any{
			"kind":         "tool",
			"status":       "start",
			"tool":         ev.Tool,
			"tool_call_id": ev.ToolCallID,
			"args":         ev.Args,
			"run_id":       ev.RunID,
		}
	case mow.EventToolEnd:
		out := map[string]any{
			"kind":         "tool",
			"status":       "end",
			"tool":         ev.Tool,
			"tool_call_id": ev.ToolCallID,
			"run_id":       ev.RunID,
		}
		if ev.Denied {
			out["denied"] = true
		}
		if ev.Error != "" {
			out["error"] = ev.Error
		}
		if ev.Result != "" {
			out["result"] = ev.Result
		}
		return out
	case mow.EventRunStart:
		return map[string]any{"kind": "state", "state": "running", "run_id": ev.RunID}
	case mow.EventRunEnd:
		out := map[string]any{
			"kind":        "state",
			"state":       "idle",
			"run_id":      ev.RunID,
			"stop_reason": ev.StopReason,
		}
		if ev.InputTokens > 0 || ev.OutputTokens > 0 {
			out["usage"] = map[string]any{
				"input_tokens":  ev.InputTokens,
				"output_tokens": ev.OutputTokens,
			}
		}
		return out
	default:
		if ev.InputTokens > 0 || ev.OutputTokens > 0 {
			if strings.HasPrefix(string(ev.Type), "loop.") {
				return map[string]any{
					"kind":   "usage",
					"run_id": ev.RunID,
					"usage": map[string]any{
						"input_tokens":  ev.InputTokens,
						"output_tokens": ev.OutputTokens,
					},
				}
			}
		}
		return nil
	}
}
