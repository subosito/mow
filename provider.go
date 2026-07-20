package mow

import "context"

// ChatHooks are streaming callbacks a Provider may invoke during Chat.
// Either may be nil; implementations that do not stream simply ignore them.
type ChatHooks struct {
	// OnToken receives answer content deltas.
	OnToken func(delta string)
	// OnReasoning receives thinking deltas (UI-only; never part of history).
	OnReasoning func(delta string)
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
