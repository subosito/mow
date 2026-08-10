package agent

import (
	"context"
	"encoding/json"
	"time"

	"github.com/subosito/mow/internal/llm"
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
// Messages is the current history (read-only intent — do not mutate). Hooks may
// use it to build a better Summary than the default char stub.
type PreCompactEvent struct {
	EstChars int
	MaxChars int
	// CharsPerToken is the calibrated chars/token ratio used to derive
	// EstChars from raw history size (seeded at 4, clamped to [2,8]).
	CharsPerToken float64
	Messages      []llm.Message
}

// PreCompactDecision may skip compaction or supply the stub summary text.
// Summary replaces the default compact note (task anchors still applied).
type PreCompactDecision struct {
	Skip    bool
	Summary string
}

// PreCompactFunc runs before Compact when MaxContextChars is set and history is over budget.
type PreCompactFunc func(ctx context.Context, e PreCompactEvent) (PreCompactDecision, error)

// AfterCompactEvent reports one automatic context projection compaction.
// Layer is "snip" when reducing tool bodies was sufficient, or "drop" when
// completed older turns were also replaced by anchors and a summary. OverBudget
// is true when all layers ran but the projection still exceeds its target.
type AfterCompactEvent struct {
	Layer          string
	CharsBefore    int
	CharsAfter     int
	CharsSaved     int
	MessagesBefore int
	MessagesAfter  int
	OverBudget     bool
}

// AfterCompactFunc runs after the projection has been compacted.
type AfterCompactFunc func(ctx context.Context, e AfterCompactEvent)

// AfterTurnEvent is emitted after each LLM assistant message is appended.
type AfterTurnEvent struct {
	AssistantText string
	HasToolCalls  bool
}

// AfterTurnFunc runs after each LLM turn (tools may still follow).
type AfterTurnFunc func(ctx context.Context, e AfterTurnEvent)

// PreModelEvent is emitted immediately before each LLM call.
//
// This is the loop's only gate on the model call itself. Tools have had
// PreTool since the beginning, but the call that actually spends money had no
// seam: a policy could refuse `bash` and could not refuse another round trip.
//
// Deliberately does NOT carry Messages. PreCompact already owns history
// rewriting and runs a few statements earlier; handing the live `send` slice
// to a second hook this close to the wire invites mutation that would desync
// SentChars, the chars/token calibration, and compaction's bookkeeping. A hook
// that needs history shape has PreCompact and AfterTurn.
type PreModelEvent struct {
	// Turn is the 1-based loop index for the call about to be made.
	Turn int
	// Usage is the running provider-reported total for this run so far
	// (zero on the first turn — nothing has been billed yet).
	Usage llm.Usage
	// SentChars is the estimated size of this request: history projection
	// plus serialized tool definitions. Already computed by the loop.
	SentChars int
	// CharsPerToken is the calibrated ratio used to turn SentChars into a
	// token estimate (seeded at 4, clamped to [2,8]).
	CharsPerToken float64
	// MaxOutputTokens is the configured cap on this reply, or 0 when unset.
	// A ceiling must bound the reply it is about to authorize, not guess it.
	MaxOutputTokens int
}

// PreModelDecision may stop the run before the call is made.
//
// There is deliberately no way to rewrite the outgoing request: that is
// compaction's job, it already ran, and a second mutation point here would
// fight it.
type PreModelDecision struct {
	// Stop ends the run cleanly before the call. Partial history is kept.
	Stop bool
	// Reason is surfaced in the returned error. Say what limit was hit.
	Reason string
}

// PreModelFunc runs before each LLM call. The first hook returning Stop wins
// and the rest are skipped. Returning an error aborts the whole Run — same
// contract as PreTool: a policy gate that cannot evaluate must fail closed,
// because failing open on the spend path is the failure it exists to prevent.
type PreModelFunc func(ctx context.Context, e PreModelEvent) (PreModelDecision, error)

// Hooks are optional lifecycle callbacks (UI, metrics, context optimizers).
type Hooks struct {
	PreModel     []PreModelFunc
	PreTool      []PreToolFunc
	PostTool     []PostToolFunc
	PreCompact   []PreCompactFunc
	AfterCompact []AfterCompactFunc
	AfterTurn    []AfterTurnFunc
}
