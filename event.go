package mow

import (
	"context"
	"encoding/json"
	"time"
)

// context key for EngineFromContext (tools / packs during Prompt).
type engineCtxKey struct{}

// ContextWithEngine returns ctx carrying eng for EngineFromContext.
func ContextWithEngine(ctx context.Context, eng *Engine) context.Context {
	if eng == nil {
		return ctx
	}
	return context.WithValue(ctx, engineCtxKey{}, eng)
}

// EngineFromContext returns the Engine running the current Prompt, if any.
func EngineFromContext(ctx context.Context) *Engine {
	if ctx == nil {
		return nil
	}
	eng, _ := ctx.Value(engineCtxKey{}).(*Engine)
	return eng
}

// EventType identifies a structured run lifecycle event for hosts/orchestrators.
type EventType string

const (
	EventRunStart      EventType = "run.start"
	EventToken         EventType = "token"     // answer content delta
	EventReasoning     EventType = "reasoning" // reasoning delta (UI/host optional)
	EventToolStart     EventType = "tool.start"
	EventToolEnd       EventType = "tool.end"
	EventTurn          EventType = "turn"           // assistant message after LLM step
	EventDelegateChunk EventType = "delegate.chunk" // peer ACP text delta
	EventRunEnd        EventType = "run.end"
)

// Stop reasons for EventRunEnd / RunResult.StopReason.
const (
	StopCompleted = "completed"
	StopCancelled = "cancelled"
	StopMaxTurns  = "max_turns"
	StopError     = "error"
)

// Event is one structured notification during Engine.Prompt.
// JSON field names are stable for rpc notifications and host parsers.
type Event struct {
	Type      EventType `json:"type"`
	RunID     string    `json:"run_id"`
	SessionID string    `json:"session_id,omitempty"`
	TS        time.Time `json:"ts"`

	// Prompt text (run.start) or final assistant text (run.end).
	Text string `json:"text,omitempty"`
	// Streaming deltas (token / reasoning / delegate.chunk).
	Delta string `json:"delta,omitempty"`

	// Tool fields
	Tool       string          `json:"tool,omitempty"`
	ToolCallID string          `json:"tool_call_id,omitempty"`
	Args       json.RawMessage `json:"args,omitempty"`
	Result     string          `json:"result,omitempty"` // may be truncated for size
	Denied     bool            `json:"denied,omitempty"`
	Error      string          `json:"error,omitempty"`

	// Turn / run completion
	HasToolCalls bool   `json:"has_tool_calls,omitempty"`
	StopReason   string `json:"stop_reason,omitempty"`
	// Delegate
	Agent string `json:"agent,omitempty"`
}

// EventFunc receives lifecycle events. Must not block long.
// Multiple listeners: Engine.AddOnEvent (fan-out); SetOnEvent replaces all.
type EventFunc func(Event)

// Status is a snapshot of Engine control-plane state (rpc status, health checks).
type Status struct {
	Busy       bool   `json:"busy"`
	RunID      string `json:"run_id,omitempty"`
	SessionID  string `json:"session_id,omitempty"`
	Workspace  string `json:"workspace,omitempty"`
	Model      string `json:"model,omitempty"`
	Wire       string `json:"wire,omitempty"`
	AllowWrite bool   `json:"allow_write"`
	AllowShell bool   `json:"allow_shell"`
}
