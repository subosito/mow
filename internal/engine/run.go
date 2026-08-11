// Package mow is the public agentic harness library: Engine API for any UI or embedder.
//
// Public surface:
//   - mow.New / Engine / Run — programmatic harness
//   - mow.Provider — swap the LLM backend (streaming + usage preserved)
//   - Options.HTTPClient / Options.Logger — inject transport + structured logs
//   - mow.Tool / Options.Tools / Hooks — integration types (per-engine tools)
//   - RunResult.Usage / Event tokens — provider-reported token accounting
//   - ext / ext/* — optional packs (acp, rpc, mcp, lsp, …)
//   - cliutil / extcfg — host helpers (not packs)
//
// Implementation lives under internal/ (agent loop, llm, tools, config, …).
package engine

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/subosito/mow/internal/agent"
)

// ErrAgentDone is returned by a tool to end the current Prompt successfully
// after the tool batch (e.g. goal_report). Same as internal agent.ErrDone.
var ErrAgentDone = agent.ErrDone

// ErrAgentStuck is returned when the model repeats the same tool calls too many times.
var ErrAgentStuck = agent.ErrStuck

// ErrAgentMaxTurns is returned when a Prompt hits MaxTurns or the unlimited safety cap.
var ErrAgentMaxTurns = agent.ErrMaxTurns

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
	// Workspace overrides config/env workspace when non-empty. Hybrid: a set
	// name from $MOW_HOME/workspaces.yaml (root + extra_roots) or a plain
	// directory path. A matched set name wins over an existing directory of
	// the same name.
	Workspace string
	// ExtraRoots appends directory trees to the FS path jail (in addition to
	// Workspace). Relative paths resolve against the process cwd at New.
	// Same as policy.extra_roots / repeatable --extra-root. CLI/config may use
	// PATH:ro for read-only roots (see ExtraRootsReadOnly). Not settable from
	// project .mow/config.
	ExtraRoots []string
	// ExtraRootsReadOnly appends read-only FS jail roots. Prefer CLI/config
	// "PATH:ro" on ExtraRoots/--extra-root; this field is for programmatic hosts.
	// Write/edit is denied under these roots even when AllowWrite is true.
	ExtraRootsReadOnly []string
	// Model overrides config/env model when non-empty.
	Model string
	// ExplicitModel marks Model as a explicit user/CLI override (e.g. --model).
	// When resuming a session, an explicit model wins over the session's stored
	// model, whereas a default/config-provided Model yields to the session.
	ExplicitModel bool
	// Effort overrides config/env reasoning effort when non-empty
	// (none|low|medium|high). Empty leaves config.
	Effort string
	// ExplicitEffort marks Effort as an explicit user/CLI override (e.g.
	// --effort). When resuming a session, an explicit effort wins over the
	// session's stored effort, whereas a default/config-provided Effort yields
	// to the session.
	ExplicitEffort bool
	// BaseURL overrides config/env LLM base URL when non-empty.
	BaseURL string
	// SystemPrefix prepends optional identity text before the compiled system
	// prompt. Each entry is a separate system segment.
	SystemPrefix []string
	// AllowWrite / AllowShell override config enable list when true.
	AllowWrite bool
	AllowShell bool
	// NoSession skips JSONL persistence.
	NoSession bool
	// SessionID forces a session id (resume that file).
	SessionID string
	// Continue loads the latest session under the project dir when SessionID empty.
	Continue bool
	// MaxTurns overrides config when non-zero: positive = that many turns,
	// negative (e.g. -1) = unlimited. Zero leaves config (default 120).
	// CLI --max-turns 0 maps to -1 (unlimited).
	MaxTurns int
	// Extra system text appended after AGENTS.md.
	SystemAppend string
	// ExplicitSkills names skill folders to load unconditionally, regardless
	// of the first-prompt selector (skills.selector). Names match folder names
	// case-insensitively. Unknown names are silently ignored. CLI --skill
	// appends here; config skills.explicit is merged separately.
	ExplicitSkills []string
	// SkillsDirs adds extra skill directories searched (after the global and
	// config dirs). Lets tests and hosts inject a temp skill dir without
	// writing config. Empty by default.
	SkillsDirs []string
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

	// CompactSummary replaces the deterministic compaction stub with a
	// structured LLM summary (one extra call per compaction). Off by default;
	// see policy.compact_summary.
	CompactSummary bool

	// MaxRunTokens caps InputTokens+OutputTokens for one Prompt (0 = config).
	// Bounds spend, where MaxTurns only bounds round-trips.
	MaxRunTokens int
	// MaxRunUSD caps projected cost for one Prompt (0 = config). Requires
	// published pricing; New fails when the model has no price rather than
	// offering a ceiling that can never fire.
	MaxRunUSD float64
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
	// CachedInputTokens is the share of InputTokens served from the provider's
	// prompt cache (a subset, not an addition). Billed at a large discount, so
	// Cost prices it separately.
	CachedInputTokens int
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
	// ExtraTools are available only for this Prompt (appended after engine tools).
	// Used by packs like goal for goal_report so the tool is not always visible
	// in repl/run (where it used to encourage false "task complete" reports).
	ExtraTools []Tool
	// Ephemeral runs the turn against the current context but does NOT persist
	// it: the question and answer are not appended to the in-memory history
	// (e.prior/transcript) or the session file, so they never re-enter a later
	// prompt. Streaming, events, and hooks still fire so a UI can render the
	// reply. Use for mid-conversation asides ("/btw …") that must not pollute
	// context. Orthogonal to side effects — combine with ReadOnly to also
	// forbid writes/shell during the aside.
	Ephemeral bool
}

// Run is a one-shot helper: New + single Prompt. Close is deferred so
// session-scoped resources (e.g. ext/proc background processes) are torn down
// when the one-shot finishes.
func Run(ctx context.Context, prompt string, opt Options) (RunResult, error) {
	eng, err := New(opt)
	if err != nil {
		return RunResult{}, err
	}
	defer eng.Close()
	return eng.Prompt(ctx, prompt)
}
