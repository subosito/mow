// Package mow is the public agentic harness library: Engine API for any UI or embedder.
//
// Public surface:
//   - mow.New / Engine / Run — programmatic harness
//   - mow.Provider — swap the LLM backend (streaming + usage preserved)
//   - Options.HTTPClient / Options.Logger — inject transport + structured logs
//   - mow.Tool / Options.Tools / Hooks — integration types (per-engine tools)
//   - RunResult.Usage / Event tokens — provider-reported token accounting
//   - ext / ext/* — optional packs (acp, rpc, mcp, lsp, …)
//   - cliutil / packcfg — host helpers (not packs)
//
// Implementation lives under internal/ (agent loop, llm, tools, config, …).
package mow

import (
	"context"
	"log/slog"
	"net/http"
)

// Options configures New / Run.
type Options struct {
	ConfigPaths []string
	// HTTPClient is used for all LLM/media HTTP (proxies, custom timeouts,
	// transport middleware). Nil uses a default client (120s chat, 180s media).
	HTTPClient *http.Client
	// Logger receives engine logs (run/tool/warn). Nil uses slog.Default().
	// Set a discarding handler to silence, or your own to capture structured
	// logs without touching the process-global default.
	Logger *slog.Logger
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
	// Tools are engine-scoped custom tools, unlike the process-global
	// ext.RegisterTool: two Engines in one process can run different toolsets.
	// A per-engine tool overrides a registry tool of the same name; colliding
	// with a builtin name is an error (the jailed builtins cannot be
	// replaced). Implement `ReadOnly() bool` to stay usable in read-only
	// prompts.
	Tools []Tool
	// Provider swaps the LLM backend (streaming, tool calls, usage all work).
	// Implement the optional ModelLister/ModelSwitcher extensions to keep
	// ListModels/SetModel functional. Takes precedence over Chat.
	Provider Provider
	// Chat injects a bare chat function (tests / quick fakes). Streaming
	// callbacks never fire on this path — prefer Provider for real backends.
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
	// Usage is provider-reported tokens summed across every LLM call in the
	// run (zero when the provider sent none).
	Usage Usage
}

// Usage is provider-reported token counts.
type Usage struct {
	InputTokens  int
	OutputTokens int
}

// PromptOpts configures a single Prompt call (not Engine lifetime).
type PromptOpts struct {
	// SystemAppend is merged into the system prompt for this call only
	// (after config/skills/SessionStart appends).
	SystemAppend string
	// ReadOnly allows only side-effect-free tools for this call (ACP "ask"
	// mode): builtin read/glob/grep, understand_*, and ext tools that declare
	// ReadOnly() true (e.g. MCP tools with readOnlyHint). Everything else —
	// including pack and MCP tools without the marker — is denied.
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
