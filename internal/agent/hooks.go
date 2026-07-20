package agent

import (
	"context"
	"encoding/json"
	"time"
)

// PreToolEvent is emitted before a tool Exec.
type PreToolEvent struct {
	Name       string
	Args       json.RawMessage
	ToolCallID string
}

// PreToolDecision may deny, rewrite args, or attach context for the model.
type PreToolDecision struct {
	Deny              bool
	Message           string
	Args              json.RawMessage
	RewriteArgs       bool
	AdditionalContext string
}

// PreToolFunc runs before each tool call. Returning error aborts the whole Run.
type PreToolFunc func(ctx context.Context, e PreToolEvent) (PreToolDecision, error)

// PostToolEvent is emitted after Exec (or after deny).
type PostToolEvent struct {
	Name       string
	Args       json.RawMessage
	ToolCallID string
	Result     string
	Denied     bool
	ExecErr    error
	// Duration is wall time for this tool (hooks + Exec), when measured.
	Duration time.Duration
}

// PostToolDecision may replace the tool result string shown to the model.
type PostToolDecision struct {
	Result  string
	Rewrite bool
}

// PostToolFunc runs after each tool call.
type PostToolFunc func(ctx context.Context, e PostToolEvent) (PostToolDecision, error)

// PreCompactEvent is emitted when soft history compaction is about to run.
type PreCompactEvent struct {
	EstChars int
	MaxChars int
}

// PreCompactDecision may skip compaction or supply the stub summary text.
type PreCompactDecision struct {
	Skip    bool
	Summary string
}

// PreCompactFunc runs before Compact when MaxContextChars is set.
type PreCompactFunc func(ctx context.Context, e PreCompactEvent) (PreCompactDecision, error)

// AfterTurnEvent is emitted after each LLM assistant message is appended.
type AfterTurnEvent struct {
	AssistantText string
	HasToolCalls  bool
}

// AfterTurnFunc runs after each LLM turn (tools may still follow).
type AfterTurnFunc func(ctx context.Context, e AfterTurnEvent)

// Hooks are optional lifecycle callbacks (UI, metrics, context optimizers).
type Hooks struct {
	PreTool    []PreToolFunc
	PostTool   []PostToolFunc
	PreCompact []PreCompactFunc
	AfterTurn  []AfterTurnFunc
}
