// Package mow is the public agentic harness library: Engine API for any UI or embedder.
//
// Public surface:
//   - mow.New / Engine / Run — programmatic harness
//   - mow.Tool / Hooks / ChatFunc — integration types
//   - ext / ext/* — optional packs (acp, rpc, mcp, lsp, …)
//   - cliutil / packcfg — host helpers (not packs)
//
// Implementation lives under internal/ (agent loop, llm, tools, config, …).
package mow

import "context"

// Options configures New / Run.
type Options struct {
	ConfigPaths []string
	// Workspace overrides config/env workspace when non-empty.
	Workspace string
	// Model overrides config/env model when non-empty.
	Model string
	// BaseURL overrides config/env LLM base URL when non-empty.
	BaseURL string
	// AllowWrite / AllowShell override config enable list when true.
	AllowWrite bool
	AllowShell bool
	// NoSession skips JSONL persistence.
	NoSession bool
	// SessionID forces a session id (resume that file).
	SessionID string
	// Continue loads the latest session under the project dir when SessionID empty.
	Continue bool
	// MaxTurns overrides config when > 0.
	MaxTurns int
	// Extra system text appended after AGENTS.md.
	SystemAppend string
	// Chat injects a fake LLM (tests).
	Chat ChatFunc
	// Stream enables SSE token deltas when using the default OpenAI client.
	Stream bool
	// OnToken receives content (answer) deltas when streaming (UI).
	OnToken func(delta string)
	// OnReasoning receives reasoning/thinking deltas when streaming (UI-only).
	OnReasoning func(delta string)
	// OnEvent receives structured lifecycle events (run/tool/token/turn).
	// Registered via AddOnEvent at New; use Engine.AddOnEvent for additional listeners.
	OnEvent EventFunc
	// Hooks optional lifecycle callbacks (merged with ext global hooks).
	Hooks Hooks
	// MaxContextChars overrides config soft history compaction (0 = use config).
	// Config default is ~100k chars; set policy max_context_chars: -1 to disable.
	MaxContextChars int
	// MaxToolResultChars overrides config cap on tool results in history (0 = config).
	MaxToolResultChars int
}

// RunResult is the outcome of one Prompt / Run.
type RunResult struct {
	Text       string
	SessionID  string
	RunID      string // correlates with Event.RunID for this Prompt
	StopReason string // completed | cancelled | max_turns | error
}

// PromptOpts configures a single Prompt call (not Engine lifetime).
type PromptOpts struct {
	// SystemAppend is merged into the system prompt for this call only
	// (after config/skills/SessionStart appends).
	SystemAppend string
	// ReadOnly denies write/edit/bash for this call only (ACP "ask" mode).
	ReadOnly bool
}

// Run is a one-shot helper: New + single Prompt.
func Run(ctx context.Context, prompt string, opt Options) (RunResult, error) {
	eng, err := New(opt)
	if err != nil {
		return RunResult{}, err
	}
	return eng.Prompt(ctx, prompt)
}
