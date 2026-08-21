package engine

import (
	"context"
	"time"
)

// ChatHooks are streaming callbacks a Provider may invoke during Chat.
// Any of them may be nil; implementations that do not stream simply ignore
// them.
type ChatHooks struct {
	// OnToken receives answer content deltas.
	OnToken func(delta string)
	// OnReasoning receives thinking deltas (UI-only; never part of history).
	OnReasoning func(delta string)
	// OnActivity should be invoked on the first upstream frame of any kind —
	// including tool-call frames that carry no content/reasoning — so the
	// engine can end its pre-first-byte wait before Chat returns. Never pass
	// tool arguments or frame payloads; it is a bare signal. Providers that
	// do not stream may leave it uncalled: a successful return ends the wait.
	OnActivity func()
	// OnRetry, when non-nil, should be invoked once per scheduled retry
	// (before the backoff sleep) with a classified, secret-free RetryInfo so
	// hosts can show honest backoff copy instead of "gateway silent".
	OnRetry func(RetryInfo)
}

// RetryKind classifies a scheduled model-call retry for host display copy.
type RetryKind int

const (
	// RetryBusy: the gateway answered a retryable status (429/5xx) — alive
	// but overloaded.
	RetryBusy RetryKind = iota
	// RetryUnavailable: connection refused/reset — the gateway is down or
	// restarting.
	RetryUnavailable
	// RetryNetwork: any other transient transport error.
	RetryNetwork
)

// RetryInfo describes one scheduled retry of the model call. It must never
// carry URLs, headers, credentials, or request/response bodies.
type RetryInfo struct {
	// Attempt is the 1-based ordinal of the upcoming retry (first retry = 1).
	Attempt int
	// Delay is the backoff sleep before the next attempt starts.
	Delay time.Duration
	// Status is the retryable HTTP status (429/5xx), or 0 for
	// transport-level failures and in-body overload signals.
	Status int
	Kind   RetryKind
}

// Provider is the LLM seam: one call per model turn. Implementations return
// the final assistant message — Content, ToolCalls, and the response-only
// StopReason/Usage fields — and may stream deltas through hooks as they go.
//
// Set via Options.Provider. Unlike the legacy Options.Chat func, a Provider
// keeps token streaming working (hooks are wired to Engine.SetOnToken /
// OnEvent), and may implement the optional extensions below to keep
// Engine.ListModels / SetModel working too.
type Provider interface {
	Chat(ctx context.Context, messages []Message, tools []ToolSpec, hooks ChatHooks) (Message, error)
}

// ModelLister is an optional Provider extension backing Engine.ListModels.
type ModelLister interface {
	ListModels(ctx context.Context) ([]ModelInfo, error)
}

// ModelSwitcher is an optional Provider extension backing Engine.SetModel.
type ModelSwitcher interface {
	SetModel(id string) error
}
