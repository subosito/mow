package engine

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
	// EventCompact reports projection-only context reduction. See CompactLayer.
	EventCompact EventType = "loop.compact"
	// EventStall is emitted once when the loop stops early because consecutive
	// tool batches added no new evidence. Text carries the reason; the run
	// ends with StopStuck.
	EventStall EventType = "loop.stall"
	// EventSteer: host injected guidance into a running turn; the in-flight
	// LLM call was interrupted and will be reissued with the steer appended.
	EventSteer  EventType = "loop.steer"
	EventRunEnd EventType = "loop.run.end"

	// graph.* — orchestration (ext/goal pack): state transitions and node progress.
	EventGoalStart   EventType = "graph.goal.start"
	EventGoalStep    EventType = "graph.goal.step"
	EventGoalDone    EventType = "graph.goal.done"
	EventGoalFail    EventType = "graph.goal.fail"
	EventGoalPartial EventType = "graph.goal.partial"
	EventGoalBlocked EventType = "graph.goal.blocked"
	EventToolStart   EventType = "harness.tool.start"
	EventToolEnd     EventType = "harness.tool.end"
	// EventContextSinkStore reports that a large successful tool result was
	// moved out of live history into the session-scoped result store.
	EventContextSinkStore EventType = "harness.contextsink.store"

	// EventCompactSummary reports the cost of one opt-in compaction summary
	// call (policy.compact_summary). Surfaced separately from run totals so
	// the extra spend is attributable — this call is the whole reason the
	// feature is opt-in.
	EventCompactSummary EventType = "harness.compact.summary"
	// EventContextSinkRecover reports bytes returned to live context by
	// context_search, either by stored-result ID or archive pattern search.
	EventContextSinkRecover EventType = "harness.contextsink.recover"
	EventDelegateChunk      EventType = "harness.delegate.chunk"    // peer ACP answer text delta
	EventDelegateProgress   EventType = "harness.delegate.progress" // peer tool/thought status (not final answer)
	// EventDelegateUsage: provider-reported tokens for one completed delegated
	// call (InputTokens/OutputTokens + Agent). Lets hosts show true spend
	// including native mow peers.
	EventDelegateUsage EventType = "harness.delegate.usage"
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
	//	    {
	//	      "severity": "error",         // error|warning|information|hint
	//	      "message": "undefined: foo",
	//	      "line": 42,                  // 1-based
	//	      "column": 9,                 // 1-based; 0 when the server omits it
	//	      "source": "compiler"         // producing tool, "" when absent
	//	    }
	//	  ]
	//	}
	//
	// Diagnostics are sorted most severe first, then truncated, so a host that
	// renders only the head still sees the errors. count may exceed
	// len(diagnostics) when the list was truncated.
	EventLSPDiagnostics EventType = "harness.lsp.diagnostics"
)

// CompactLayer identifies the most expensive projection layer used.
type CompactLayer string

const (
	CompactLayerSnip CompactLayer = "snip"
	CompactLayerDrop CompactLayer = "drop"
)

// MaxLSPDiagnostics bounds how many findings ride along a tool result and an
// EventLSPDiagnostics payload.
const MaxLSPDiagnostics = 10

// DiagnosticSeverity is the severity vocabulary of EventLSPDiagnostics.
type DiagnosticSeverity string

// Diagnostic severities, most severe first (see SeverityRank).
const (
	SeverityError       DiagnosticSeverity = "error"
	SeverityWarning     DiagnosticSeverity = "warning"
	SeverityInformation DiagnosticSeverity = "information"
	SeverityHint        DiagnosticSeverity = "hint"
)

// SeverityRank orders severities for display and truncation: lower is more
// severe. Unknown values sort last.
func SeverityRank(s DiagnosticSeverity) int {
	switch s {
	case SeverityError:
		return 0
	case SeverityWarning:
		return 1
	case SeverityInformation:
		return 2
	case SeverityHint:
		return 3
	}
	return 4
}

// Diagnostic is one language-server finding (see EventLSPDiagnostics).
type Diagnostic struct {
	Severity DiagnosticSeverity `json:"severity"`
	Message  string             `json:"message"`
	Line     int                `json:"line"`             // 1-based
	Column   int                `json:"column,omitempty"` // 1-based; 0 when the server omits it
	// Source is the producing tool as reported by the server (e.g. "compiler",
	// "staticcheck"); empty when absent.
	Source string `json:"source,omitempty"`
}

// Stop reasons for EventRunEnd / RunResult.StopReason.
const (
	StopCompleted = "completed"
	StopCancelled = "cancelled"
	StopMaxTurns  = "max_turns"
	StopStuck     = "stuck"
	// StopBudget: a PreModel gate refused another LLM call — in practice the
	// spend ceiling (policy.max_run_tokens / max_run_usd). Partial work is in
	// the session; this is NOT task completion.
	StopBudget = "budget"
	// StopTruncated: the provider cut the final reply at its token limit and
	// left no usable text (raise llm.max_tokens).
	StopTruncated = "truncated"
	StopError     = "error"
)

// GoalNode is one checklist node projection for GoalEvent.
type GoalNode struct {
	ID     string `json:"id"`
	Title  string `json:"title,omitempty"`
	Status string `json:"status"`
}

// GoalEvent is the goal orchestration payload projection for Event.
type GoalEvent struct {
	ID       string     `json:"id"`
	Status   string     `json:"status"`
	Step     int        `json:"step,omitempty"`
	MaxSteps int        `json:"max_steps,omitempty"`
	Summary  string     `json:"summary,omitempty"`
	Nodes    []GoalNode `json:"nodes,omitempty"`
}

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
	// CachedInputTokens is the share of InputTokens the provider served from
	// its prompt cache (a subset, not an addition). Hosts can show cache
	// effectiveness; a sudden drop means the prefix stopped being stable.
	CachedInputTokens int `json:"cached_input_tokens,omitempty"`
	// CacheWriteInputTokens is the share written INTO the cache this run.
	// A large value relative to CachedInputTokens means the prefix is being
	// invalidated and re-uploaded rather than extended — the expensive
	// failure mode, and usually a symptom of something perturbing the prefix
	// (model switch, compaction, tool-set change).
	CacheWriteInputTokens int `json:"cache_write_input_tokens,omitempty"`
	// ProviderToolCalls counts tools the provider executed server-side for
	// this run (native_tools, e.g. web_search). They never enter the tool
	// loop, so they raise no tool.start/tool.end — without this a host has no
	// way to show work the provider did and billed for.
	ProviderToolCalls int `json:"provider_tool_calls,omitempty"`
	// Delegate
	Agent string `json:"agent,omitempty"`

	// Context compaction (loop.compact). Counts are raw characters/messages;
	// OverBudget means all layers ran but the projection still exceeds target.
	Layer          CompactLayer `json:"layer,omitempty"`
	CharsBefore    int          `json:"chars_before,omitempty"`
	CharsAfter     int          `json:"chars_after,omitempty"`
	CharsSaved     int          `json:"chars_saved,omitempty"`
	MessagesBefore int          `json:"messages_before,omitempty"`
	MessagesAfter  int          `json:"messages_after,omitempty"`
	OverBudget     bool         `json:"over_budget,omitempty"`

	// Goal Event Payload (graph.goal.*)
	//
	// Frozen payload shape (host contract for goal state/progress):
	//
	//	{
	//	  "type": "graph.goal.step",
	//	  "run_id": "run-...",
	//	  "session_id": "2026...",
	//	  "goal": {
	//	    "id": "fix-bugs",
	//	    "status": "running",
	//	    "step": 2,
	//	    "max_steps": 10,
	//	    "summary": "step summary...",
	//	    "nodes": [
	//	      {"id": "a", "title": "analyze", "status": "done"}
	//	    ]
	//	  }
	//	}
	Goal *GoalEvent `json:"goal,omitempty"`

	// LSP diagnostics (harness.lsp.diagnostics). Path is the edited file,
	// Count the server total, Diagnostics bounded by MaxLSPDiagnostics.
	Path        string       `json:"path,omitempty"`
	Count       int          `json:"count,omitempty"`
	Diagnostics []Diagnostic `json:"diagnostics,omitempty"`

	// Context sink (harness.contextsink.store/recover). These fields contain
	// sizes and stable IDs only, never the stored tool-result body.
	StoredID       string `json:"stored_id,omitempty"`
	OriginalBytes  int    `json:"original_bytes,omitempty"`
	InlineBytes    int    `json:"inline_bytes,omitempty"`
	RecoveredBytes int    `json:"recovered_bytes,omitempty"`
	RecoveryMode   string `json:"recovery_mode,omitempty"` // "id" or "pattern"
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
