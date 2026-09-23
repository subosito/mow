package acp

import (
	"encoding/json"
	"strings"
	"sync/atomic"

	"github.com/subosito/mow"
)

// liveForward streams one engine run to an ACP session: model text, tool
// calls, delegate/peer activity, file changes, compaction, model status, goal
// progress, and usage. session/prompt and exclusive slash commands (/goal)
// both use it, so a goal driving many engine turns shows the same live
// progress as a single prompt instead of going silent until it finishes.
type liveForward struct {
	streamed  atomic.Bool
	writeText func(string)
	stop      func()
}

// startLiveForward installs the token callback and event listener for sid.
// Call stop exactly once when the run ends.
func (a *agentServer) startLiveForward(sid string) *liveForward {
	f := &liveForward{}
	f.writeText = func(text string) {
		if text == "" {
			return
		}
		f.streamed.Store(true)
		a.write(notification{
			JSONRPC: "2.0",
			Method:  "session/update",
			Params: mustJSON(sessionUpdateParams{
				SessionID: sid,
				Update: sessionUpdate{
					SessionUpdate: "agent_message_chunk",
					Content:       &chunkContent{Type: "text", Text: text},
				},
			}),
		})
	}
	// writeToolCall emits ACP tool_call / tool_call_update so hosts can show
	// "read engine.go" / "grep foo in pkg/" instead of bare "→ read".
	writeToolCall := func(updateKind, callID, kind, title, status string, args json.RawMessage, result, errorText, errorCode string, executed bool, durationMs int64) {
		if kind == "" && title == "" {
			return
		}
		update := map[string]any{
			"sessionUpdate": updateKind,
			"toolCallId":    callID,
			"kind":          kind,
			"title":         title,
			"status":        status,
		}
		if len(args) > 0 && string(args) != "null" {
			update["rawInput"] = json.RawMessage(args)
		}
		if durationMs > 0 {
			update["duration_ms"] = durationMs
		}
		if errorText != "" {
			update["error"] = errorText
		}
		if errorCode != "" {
			update["error_code"] = errorCode
		}
		if updateKind == "tool_call_update" {
			update["executed"] = executed
			update["is_error"] = status == "failed"
		}
		if result != "" && toolCallAttachesResult(kind) {
			update["rawOutput"] = result
			update["content"] = []map[string]any{
				{"type": "content", "content": map[string]any{"type": "text", "text": result}},
			}
		}
		a.emitSessionUpdate(sid, update)
	}
	a.eng.SetOnToken(f.writeText)
	unsub := a.eng.AddOnEvent(func(ev mow.Event) {
		switch ev.Type {
		case mow.EventDelegateChunk:
			// A nested delegate's answer is NOT this agent's reply.
			// Forward as an extra update so the ACP host's /peers view can paint it
			// without committing it as the host answer.
			agent := strings.TrimSpace(ev.Agent)
			line := strings.TrimSpace(ev.Delta)
			if line != "" {
				a.emitSessionUpdate(sid, map[string]any{
					"sessionUpdate": "delegate_chunk",
					"agent":         agent,
					"delta":         line,
				})
			}
		case mow.EventDelegateProgress:
			agent := strings.TrimSpace(ev.Agent)
			line := strings.TrimSpace(ev.Delta)
			if line == "" {
				return
			}
			a.emitSessionUpdate(sid, map[string]any{
				"sessionUpdate": "delegate_progress",
				"agent":         agent,
				"phase":         line,
			})
		case mow.EventFSChange:
			files := make([]map[string]any, 0, len(ev.Files))
			for _, file := range ev.Files {
				files = append(files, map[string]any{"path": file.Path, "op": file.Op})
			}
			a.emitSessionUpdate(sid, map[string]any{"sessionUpdate": "fs_change", "files": files})
		case mow.EventToolStart:
			kind, title := toolCallKindTitle(ev.Tool, ev.Args)
			writeToolCall("tool_call", ev.ToolCallID, kind, title, "in_progress", ev.Args, "", "", "", false, 0)
		case mow.EventToolEnd:
			kind, title := toolCallKindTitle(ev.Tool, ev.Args)
			status := "completed"
			if ev.Denied || strings.TrimSpace(ev.Error) != "" {
				status = "failed"
			}
			writeToolCall("tool_call_update", ev.ToolCallID, kind, title, status, ev.Args, ev.Result, ev.Error, ev.ErrorCode, ev.Executed, ev.DurationMs)
		case mow.EventCompactStart:
			a.emitSessionUpdate(sid, map[string]any{
				"sessionUpdate": "compact_start",
				"auto":          ev.Auto,
			})
		case mow.EventCompact:
			a.emitSessionUpdate(sid, map[string]any{
				"sessionUpdate":   "compact",
				"auto":            ev.Auto,
				"layer":           string(ev.Layer),
				"chars_before":    ev.CharsBefore,
				"chars_after":     ev.CharsAfter,
				"chars_saved":     ev.CharsSaved,
				"tokens_before":   ev.TokensBefore,
				"tokens_after":    ev.TokensAfter,
				"messages_before": ev.MessagesBefore,
				"messages_after":  ev.MessagesAfter,
				"over_budget":     ev.OverBudget,
			})
		case mow.EventModelWait:
			a.emitSessionUpdate(sid, modelStatusUpdate("model_wait", ev))
		case mow.EventModelRetry:
			a.emitSessionUpdate(sid, modelStatusUpdate("model_retry", ev))
		case mow.EventModelActive:
			a.emitSessionUpdate(sid, modelStatusUpdate("model_active", ev))
		case mow.EventGoalStart, mow.EventGoalStep, mow.EventGoalDone, mow.EventGoalFail, mow.EventGoalPartial, mow.EventGoalBlocked:
			update := map[string]any{"sessionUpdate": "goal", "type": string(ev.Type)}
			if ev.Goal != nil {
				update["goal"] = ev.Goal
			}
			if ev.Text != "" {
				update["text"] = ev.Text
			}
			a.emitSessionUpdate(sid, update)
		case mow.EventRunEnd:
			a.emitUsage(sid, ev.InputTokens, ev.OutputTokens, ev.CachedInputTokens)
		}
	})
	f.stop = func() {
		unsub()
		a.eng.SetOnToken(nil)
	}
	return f
}
