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

// Event taxonomy. Every Type carries a layer prefix so hosts and incident
// triage can answer "which layer failed?" without pattern-matching messages:
//
//	loop.*    — turn / token / run lifecycle: is another turn worth spending?
//	graph.*   — orchestration (goal pack): which state runs next?
//	harness.* — may this transition touch reality? tools, peers, diagnostics.
//
// The string values are the wire contract (rpc notifications, host parsers).
// They gained layer prefixes when the taxonomy landed ("tool.start" →
// "harness.tool.start"); switch on the constants, never on literals.
const (
	// loop.* — agent loop lifecycle.
	EventRunStart  EventType = "loop.run.start"
	EventToken     EventType = "loop.token"     // answer content delta
	EventReasoning EventType = "loop.reasoning" // reasoning delta (UI/host optional)
	EventTurn      EventType = "loop.turn"      // assistant message after LLM step
	// EventStall is emitted once when the loop stops early because consecutive
	// tool batches added no new evidence. Text carries the reason; the run
	// ends with StopStuck.
	EventStall  EventType = "loop.stall"
	EventRunEnd EventType = "loop.run.end"

	// harness.* — transitions that touch reality.
	EventToolStart        EventType = "harness.tool.start"
	EventToolEnd          EventType = "harness.tool.end"
	EventDelegateChunk    EventType = "harness.delegate.chunk"    // peer ACP answer text delta
	EventDelegateProgress EventType = "harness.delegate.progress" // peer tool/thought status (not final answer)
	// EventLSPDiagnostics reports language-server findings for a file just
	// written or edited. Emitted only when an LSP pack is configured and
	// running (no config → no process → no event).
	//
	// Frozen payload shape (host contract — e.g. a TUI Problems panel):
	//
	//	{
	//	  "type": "harness.lsp.diagnostics",
	//	  "tool": "write",            // tool that produced the edit
	//	  "path": "internal/x/y.go",  // workspace-relative when possible
	//	  "count": 3,                 // total findings reported by the server
	//	  "diagnostics": [            // bounded by MaxLSPDiagnostics
	//	    {"severity": "error", "message": "undefined: foo", "line": 42}
	//	  ]
	//	}
	//
	// severity is one of error|warning|information|hint; line is 1-based.
	// count may exceed len(diagnostics) when the list was truncated.
	EventLSPDiagnostics EventType = "harness.lsp.diagnostics"
)

// MaxLSPDiagnostics bounds how many findings ride along a tool result and an
// EventLSPDiagnostics payload.
const MaxLSPDiagnostics = 10

// Diagnostic is one language-server finding (see EventLSPDiagnostics).
type Diagnostic struct {
	Severity string `json:"severity"` // error|warning|information|hint
	Message  string `json:"message"`
	Line     int    `json:"line"` // 1-based
}

// Stop reasons for EventRunEnd / RunResult.StopReason.
const (
	StopCompleted = "completed"
	StopCancelled = "cancelled"
	StopMaxTurns  = "max_turns"
	StopStuck     = "stuck"
	// StopTruncated: the provider cut the final reply at its token limit and
	// left no usable text (raise llm.max_tokens).
	StopTruncated = "truncated"
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
	// Streaming deltas (token / reasoning / delegate.chunk / delegate.progress).
	Delta string `json:"delta,omitempty"`

	// Tool fields
	Tool       string          `json:"tool,omitempty"`
	ToolCallID string          `json:"tool_call_id,omitempty"`
	Args       json.RawMessage `json:"args,omitempty"`
	Result     string          `json:"result,omitempty"` // may be truncated for size
	Denied     bool            `json:"denied,omitempty"`
	Error      string          `json:"error,omitempty"`
	// DurationMs is wall time for the tool (tool.end only).
	DurationMs int64 `json:"duration_ms,omitempty"`

	// Turn / run completion
	HasToolCalls bool   `json:"has_tool_calls,omitempty"`
	StopReason   string `json:"stop_reason,omitempty"`
	// Provider-reported token totals for the whole run (run.end only; zero
	// when the provider sent no usage).
	InputTokens  int `json:"input_tokens,omitempty"`
	OutputTokens int `json:"output_tokens,omitempty"`
	// Delegate
	Agent string `json:"agent,omitempty"`

	// LSP diagnostics (harness.lsp.diagnostics). Path is the edited file,
	// Count the server total, Diagnostics bounded by MaxLSPDiagnostics.
	Path        string       `json:"path,omitempty"`
	Count       int          `json:"count,omitempty"`
	Diagnostics []Diagnostic `json:"diagnostics,omitempty"`
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
