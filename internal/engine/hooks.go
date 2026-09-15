package engine

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"time"

	"github.com/subosito/mow/ext"
	"github.com/subosito/mow/internal/agent"
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

// Identical shapes live in ext (registration) and here (public Options).
// Alias so mow.PreToolEvent is ext.PreToolEvent. Types that differ on
// purpose stay defined here: PostToolEvent has Duration; PreCompactEvent
// carries Messages (ext only has a count, to avoid a Message import cycle).

type PreToolEvent = ext.PreToolEvent
type PreToolDecision = ext.PreToolDecision
type PreToolFunc = ext.PreToolFunc
type PostToolDecision = ext.PostToolDecision
type UserPromptEvent = ext.UserPromptEvent
type UserPromptDecision = ext.UserPromptDecision
type UserPromptFunc = ext.UserPromptFunc
type SessionStartEvent = ext.SessionStartEvent
type SessionStartDecision = ext.SessionStartDecision
type SessionStartFunc = ext.SessionStartFunc
type PreCompactDecision = ext.PreCompactDecision
type AfterTurnEvent = ext.AfterTurnEvent
type AfterTurnFunc = ext.AfterTurnFunc
type StopEvent = ext.StopEvent
type StopFunc = ext.StopFunc

// PostToolEvent is emitted after Exec (or after deny). Duration is engine-only.
type PostToolEvent struct {
	Name       string
	Args       json.RawMessage
	ToolCallID string
	Result     string
	Denied     bool
	ExecErr    error
	Duration   time.Duration
}

// PostToolFunc runs after each tool call.
type PostToolFunc func(ctx context.Context, e PostToolEvent) (PostToolDecision, error)

// PreCompactEvent is emitted when soft history compaction is about to run.
// Messages is the current history (read-only intent). Use it to draft Summary
// (e.g. LLM distill) when the default stub is not enough; task anchors still pin user intents.
type PreCompactEvent struct {
	EstChars int
	MaxChars int
	Messages []Message
}

// PreCompactFunc runs before Compact when MaxContextChars is set and history is over budget.
type PreCompactFunc func(ctx context.Context, e PreCompactEvent) (PreCompactDecision, error)

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

// AddPreTool appends a PreTool hook (deny / rewrite args / additional context).
// The returned unsubscribe detaches the hook (safe to call once, effective for
// in-flight runs too) — hosts like TUIs must detach on shutdown or a later
// headless Prompt would stall in an approval hook nobody answers.
func (e *Engine) AddPreTool(fn PreToolFunc) (unsubscribe func()) {
	if e == nil || fn == nil {
		return func() {}
	}
	h := adaptPreTool(fn)
	var off atomic.Bool
	wrapped := func(ctx context.Context, ev agent.PreToolEvent) (agent.PreToolDecision, error) {
		if off.Load() {
			return agent.PreToolDecision{}, nil
		}
		return h(ctx, ev)
	}
	e.mu.Lock()
	e.hooks.PreTool = append(e.hooks.PreTool, wrapped)
	e.mu.Unlock()
	return func() { off.Store(true) }
}

// AddAfterTurn appends a hook that fires after each LLM turn inside a Prompt
// (HasToolCalls reports whether a tool batch follows). UIs use it to commit
// intermediate assistant text at turn boundaries instead of losing it when
// the run ends. The returned unsubscribe detaches the hook.
func (e *Engine) AddAfterTurn(fn AfterTurnFunc) (unsubscribe func()) {
	if e == nil || fn == nil {
		return func() {}
	}
	var off atomic.Bool
	wrapped := func(ctx context.Context, ev agent.AfterTurnEvent) {
		if off.Load() {
			return
		}
		fn(ctx, AfterTurnEvent{AssistantText: ev.AssistantText, HasToolCalls: ev.HasToolCalls})
	}
	e.mu.Lock()
	e.hooks.AfterTurn = append(e.hooks.AfterTurn, wrapped)
	e.mu.Unlock()
	return func() { off.Store(true) }
}

// AddPostTool appends a PostTool hook (rewrite tool result shown to the model).
// The returned unsubscribe detaches the hook (safe to call once).
func (e *Engine) AddPostTool(fn PostToolFunc) (unsubscribe func()) {
	if e == nil || fn == nil {
		return func() {}
	}
	h := adaptPostTool(fn)
	var off atomic.Bool
	wrapped := func(ctx context.Context, ev agent.PostToolEvent) (agent.PostToolDecision, error) {
		if off.Load() {
			return agent.PostToolDecision{}, nil
		}
		return h(ctx, ev)
	}
	e.mu.Lock()
	e.hooks.PostTool = append(e.hooks.PostTool, wrapped)
	e.mu.Unlock()
	return func() { off.Store(true) }
}
