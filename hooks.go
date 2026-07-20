package mow

import (
	"context"
	"encoding/json"
)

// Lifecycle hooks for the agent loop. Extensions register via ext.Register*
// or pass Hooks in Options. Enough surface for external optimizers (e.g.
// context-mode-style routing) without embedding any one product in core.
//
// Order in Engine.Prompt:
//
//	OnUserPrompt → [per LLM call: OnPreCompact?] → LLM → OnAfterTurn →
//	for each tool: OnPreTool → Exec → OnPostTool → … → OnStop
//
// OnSessionStart runs once in New after system/skills are loaded.

// PreToolEvent is emitted before a tool Exec.
type PreToolEvent struct {
	Name       string
	Args       json.RawMessage
	ToolCallID string
}

// PreToolDecision may deny, rewrite args, or attach context for the model.
type PreToolDecision struct {
	// Deny skips Exec; Message becomes the tool result (or with AdditionalContext).
	Deny    bool
	Message string
	// RewriteArgs + Args replaces tool arguments when RewriteArgs is true.
	Args        json.RawMessage
	RewriteArgs bool
	// AdditionalContext is prepended to the tool result the model sees.
	AdditionalContext string
}

// PreToolFunc runs before each tool call. Returning error aborts the whole Prompt.
type PreToolFunc func(ctx context.Context, e PreToolEvent) (PreToolDecision, error)

// PostToolEvent is emitted after Exec (or after deny).
type PostToolEvent struct {
	Name       string
	Args       json.RawMessage
	ToolCallID string
	Result     string
	Denied     bool
	ExecErr    error
}

// PostToolDecision may replace the tool result string shown to the model.
type PostToolDecision struct {
	Result  string
	Rewrite bool
}

// PostToolFunc runs after each tool call.
type PostToolFunc func(ctx context.Context, e PostToolEvent) (PostToolDecision, error)

// UserPromptEvent is emitted once at the start of Engine.Prompt.
type UserPromptEvent struct {
	Text      string
	SessionID string
	Workspace string
}

// UserPromptDecision may rewrite the user text or append system instructions.
type UserPromptDecision struct {
	Text         string
	RewriteText  bool
	SystemAppend string // this Prompt only
}

// UserPromptFunc runs before the agent loop for a user message.
type UserPromptFunc func(ctx context.Context, e UserPromptEvent) (UserPromptDecision, error)

// SessionStartEvent is emitted once when an Engine is created successfully.
type SessionStartEvent struct {
	Workspace string
	SessionID string
	Model     string
	System    string
}

// SessionStartDecision may append system text for this Engine lifetime.
type SessionStartDecision struct {
	SystemAppend string
}

// SessionStartFunc runs after Engine construction (system/skills already loaded).
type SessionStartFunc func(ctx context.Context, e SessionStartEvent) (SessionStartDecision, error)

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

// StopEvent is emitted when Prompt returns (success or error).
type StopEvent struct {
	Text      string
	Err       error
	SessionID string
}

// StopFunc runs after Prompt finishes (errors ignored).
type StopFunc func(ctx context.Context, e StopEvent)

// Hooks aggregates lifecycle callbacks (Options + ext globals are merged).
type Hooks struct {
	OnSessionStart []SessionStartFunc
	OnUserPrompt   []UserPromptFunc
	OnPreCompact   []PreCompactFunc
	OnPreTool      []PreToolFunc
	OnPostTool     []PostToolFunc
	OnAfterTurn    []AfterTurnFunc
	OnStop         []StopFunc
}
