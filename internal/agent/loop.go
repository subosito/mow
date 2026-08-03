// Package agent runs the tool-calling loop.
package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/subosito/mow/internal/llm"
)

// ErrMaxTurns is returned when the agent loop hits Options.MaxTurns (> 0).
// When MaxTurns <= 0 there is no turn limit (runs until finish, cancel, or ErrDone).
var ErrMaxTurns = errors.New("agent: max turns exceeded")

// ErrDone is returned by a tool Exec to end the agent loop successfully after
// the current tool batch (e.g. goal_report). The tool result string is still
// recorded for the model/history; Run returns nil error.
var ErrDone = errors.New("agent: done")

// ErrStuck ends a run that stopped making progress: stallBarrenBatches
// consecutive tool batches re-ran calls this run had already made for results
// it had already seen. Maps to StopStuck.
//
// Repeating the same tool calls is only a soft hint (sameToolWarnAfter); the
// hard stop is the evidence signal below. Prefer ctx cancel for a clean abort.
var ErrStuck = errors.New("agent: stuck repeating tool calls")

// ErrTruncated is returned when the provider cut the final assistant reply at
// its token limit and left no usable text (finish_reason length/max_tokens).
// Result.Messages still holds the partial history.
var ErrTruncated = errors.New("agent: response truncated at token limit")

// DefaultMaxParallelTools is used when Options.MaxParallelTools is unset (0).
const DefaultMaxParallelTools = 8

// Tool is a host-executed function.
type Tool interface {
	Name() string
	Description() string
	// Parameters is a JSON Schema object for arguments.
	Parameters() json.RawMessage
	Exec(ctx context.Context, args json.RawMessage) (string, error)
}

// ChatFn is the LLM chat primitive (injectable for tests).
type ChatFn func(ctx context.Context, messages []llm.Message, tools []llm.ToolSpec) (llm.Message, error)

// Options configures a Loop run.
type Options struct {
	System string
	// MaxTurns caps LLM round-trips when > 0. <= 0 means no turn limit
	// (hours/days OK — stop only on final text, ErrDone, error, or ctx cancel).
	MaxTurns int
	Tools    []Tool
	// PriorMessages, if non-empty, seed history before the new user prompt
	// (session resume). System is still prepended when set and not already first.
	PriorMessages []llm.Message
	// AllowTool is called before Exec; nil means always allow.
	AllowTool func(name string) error
	// Hooks optional lifecycle callbacks.
	// PreTool/PostTool may run concurrently across tools in a batch when
	// MaxParallelTools > 1 — keep them non-blocking and concurrency-safe.
	Hooks Hooks
	// OnToken is content deltas when the ChatFn streams (optional).
	OnToken func(delta string)
	// MaxContextChars soft-limits history via Compact before each LLM call (0 = off).
	MaxContextChars int
	// MaxToolResultChars caps each tool result in history (0 = DefaultMaxToolResultChars).
	MaxToolResultChars int
	// MaxParallelTools caps concurrent Exec in one assistant tool batch.
	// 0 → DefaultMaxParallelTools; 1 → sequential (legacy).
	MaxParallelTools int
	// Workspace is the project root (absolute preferred). Used by thrash
	// guards to unify absolute vs relative path re-reads.
	Workspace string
	// Steer, when set, is called at each turn boundary (after a tool batch,
	// before the next LLM call). Any returned strings are appended as user
	// messages, so a host can steer a running turn without cancelling it.
	Steer func() []string
	// SetLLMCancel, when set, registers the cancel func of the CURRENT LLM
	// call so a host can interrupt it mid-call (Engine.Steer). The loop then
	// drains Steer and reissues with the steer appended — the run survives.
	SetLLMCancel func(cancel context.CancelFunc)

	// thrash is set by Run for explore-loop / re-read guards (internal).
	thrash *thrashState
}

// Result is the final assistant text and message history.
type Result struct {
	Text     string
	Messages []llm.Message
	// Usage is provider-reported tokens summed across every LLM call in the
	// run (zero when the provider sent none).
	Usage llm.Usage
	// StopReason is the provider finish/stop reason of the final assistant
	// message ("stop", "length", "max_tokens", …); empty when the provider
	// sent none. "length"/"max_tokens" means the answer was cut off at the
	// token limit — the text is incomplete even though err is nil.
	StopReason string
}

// Run executes the agent loop until the model returns text without tool calls or max turns.
func Run(ctx context.Context, chat ChatFn, userPrompt string, opt Options) (Result, error) {
	if chat == nil {
		return Result{}, fmt.Errorf("agent: chat function required")
	}
	if strings.TrimSpace(userPrompt) == "" {
		return Result{}, fmt.Errorf("agent: empty prompt")
	}
	// <= 0: no turn limit (intentional long runs — cancel via ctx).
	maxTurns := opt.MaxTurns

	var messages []llm.Message
	sys := strings.TrimSpace(opt.System)
	if len(opt.PriorMessages) > 0 {
		messages = append(messages, opt.PriorMessages...)
		// Inject or refresh system (UserPrompt/SessionStart may have appended).
		if sys != "" {
			if len(messages) == 0 || messages[0].Role != "system" {
				messages = append([]llm.Message{{Role: "system", Content: sys}}, messages...)
			} else if messages[0].Content != sys {
				// Copy-on-write so we do not mutate the caller's PriorMessages backing array.
				messages[0].Content = sys
			}
		}
	} else if sys != "" {
		messages = append(messages, llm.Message{Role: "system", Content: sys})
	}
	messages = append(messages, llm.Message{Role: "user", Content: userPrompt})

	var usage llm.Usage
	toolSpecs := make([]llm.ToolSpec, 0, len(opt.Tools))
	byName := map[string]Tool{}
	for _, t := range opt.Tools {
		if t == nil {
			continue
		}
		name := t.Name()
		byName[name] = t
		params := t.Parameters()
		if len(params) == 0 {
			params = json.RawMessage(`{"type":"object","properties":{}}`)
		}
		toolSpecs = append(toolSpecs, llm.ToolSpec{
			Type: "function",
			Function: llm.ToolSpecFunction{
				Name:        name,
				Description: t.Description(),
				Parameters:  params,
			},
		})
	}

	var (
		lastToolFP string
		sameToolFP int
		thrash     = newThrashState(opt.Workspace)
		calib      = newRatioCalibrator()
		evidence   = newEvidenceSet()
		barrenRuns int
	)
	opt.thrash = thrash

	for turn := 0; maxTurns <= 0 || turn < maxTurns; turn++ {
		if err := ctx.Err(); err != nil {
			return Result{Messages: messages, Usage: usage}, err
		}
		var specs []llm.ToolSpec
		if len(toolSpecs) > 0 {
			specs = toolSpecs
		}
		send, err := applyCompact(ctx, messages, opt, calib)
		if err != nil {
			return Result{Messages: messages, Usage: usage}, err
		}
		sentChars := EstChars(send)
		// Per-call LLM ctx: a mid-turn steer cancels ONLY this call (via
		// opt.SetLLMCancel), never the run — the outer ctx stays alive so the
		// loop can reissue with the steer appended.
		callCtx, callCancel := context.WithCancel(ctx)
		if opt.SetLLMCancel != nil {
			opt.SetLLMCancel(callCancel)
		}
		msg, err := chat(callCtx, send, specs)
		// The child context is only needed while chat is in flight. Release its
		// parent registration on every path and stop exposing a stale cancel.
		callCancel()
		if opt.SetLLMCancel != nil {
			opt.SetLLMCancel(nil)
		}
		if err != nil {
			// Mid-turn steer: Engine.Steer cancelled this LLM call to inject
			// host guidance NOW. The OUTER ctx is still alive — drain the
			// steer into messages and reissue on the same turn; nothing is
			// lost and the run does not abort. The error must ACTUALLY be a
			// cancel: a real provider failure (network/HTTP/500) with a
			// pending steer is not an interrupt and must fail as before —
			// otherwise genuine errors get silently swallowed.
			if ctx.Err() == nil && errors.Is(err, context.Canceled) && opt.Steer != nil {
				var steers []string
				for _, s := range opt.Steer() {
					if strings.TrimSpace(s) != "" {
						steers = append(steers, s)
					}
				}
				if len(steers) > 0 {
					for _, s := range steers {
						messages = append(messages, llm.Message{Role: "user", Content: s, Synthetic: true})
					}
					continue
				}
			}
			return Result{Messages: messages, Usage: usage}, err
		}
		// Calibrate chars/token from what the provider actually billed for the
		// history we just sent, so the next pre-call budget check is empirical.
		calib.Observe(sentChars, msg.Usage.InputTokens)
		// Inline CoT normalization: models that wrap thinking in <think>-style
		// tags (instead of the reasoning channel) must never leak it into
		// committed history, sessions, or Result.Text. Stripping here also
		// keeps prior-turn CoT out of the next request's context.
		if vis, th, unclosed := extractThinking(msg.Content); th != "" || unclosed {
			msg.Content = strings.TrimSpace(vis)
		}
		messages = append(messages, msg)
		usage = usage.Add(msg.Usage)
		for _, h := range opt.Hooks.AfterTurn {
			if h != nil {
				h(ctx, AfterTurnEvent{
					AssistantText: msg.Content,
					HasToolCalls:  len(msg.ToolCalls) > 0,
				})
			}
		}

		if len(msg.ToolCalls) == 0 {
			text := strings.TrimSpace(msg.Content)
			res := Result{Text: text, Messages: messages, Usage: usage, StopReason: msg.StopReason}
			// A truncated turn with nothing usable is a silent dead end: the
			// provider cut the reply at the token limit (often mid tool-call),
			// so the loop would otherwise report "completed" with no output.
			if text == "" && msg.Truncated() {
				return res, fmt.Errorf("%w: provider stopped at the token limit with no answer (raise llm.max_tokens)", ErrTruncated)
			}
			return res, nil
		}

		// Soft: track identical batches for a hint only (never hard-stop).
		fp := toolCallFingerprint(msg.ToolCalls)
		if fp != "" && fp == lastToolFP {
			sameToolFP++
		} else {
			lastToolFP = fp
			sameToolFP = 1
		}
		exploreWarn := thrash.noteTurn(msg.ToolCalls)

		toolMsgs, err := runToolBatch(ctx, msg.ToolCalls, byName, opt)
		// Every advertised tool_call must have a matching tool result or the
		// history is unreplayable (providers 400 on orphaned tool_calls).
		// Fail-fast / cancel can drop siblings mid batch, so pad here — this
		// history is returned to the caller and persisted to the session.
		if err != nil && !errors.Is(err, ErrDone) {
			toolMsgs = repairToolResults(msg.ToolCalls, toolMsgs, err)
		}
		messages = append(messages, toolMsgs...)
		// Tool requested clean end (e.g. goal_report) — keep results, stop successfully.
		if errors.Is(err, ErrDone) {
			return Result{
				Text:       strings.TrimSpace(msg.Content),
				Messages:   messages,
				Usage:      usage,
				StopReason: msg.StopReason,
			}, nil
		}
		if err != nil {
			return Result{Messages: messages, Usage: usage}, err
		}
		// Stall detection: a batch is barren when every (call, result) pair in
		// it is one this run already produced. stallBarrenBatches in a row and
		// the loop is spinning — stop instead of burning the turn budget.
		if evidence.note(msg.ToolCalls, toolMsgs) {
			barrenRuns = 0
		} else {
			barrenRuns++
			if barrenRuns >= stallBarrenBatches {
				return Result{
						Text:       strings.TrimSpace(msg.Content),
						Messages:   messages,
						Usage:      usage,
						StopReason: msg.StopReason,
					}, fmt.Errorf("%w: %d consecutive tool batches produced no new evidence",
						ErrStuck, barrenRuns)
			}
		}
		// Soft hints only — after tool results so message order stays valid.
		// Synthetic: these are host nudges, not the user's prompt — Rewind
		// (edit/retry/↑) must skip them.
		if sameToolFP >= sameToolWarnAfter {
			messages = append(messages, llm.Message{
				Role:      "user",
				Content:   sameToolWarnMessage(sameToolFP),
				Synthetic: true,
			})
		} else if exploreWarn {
			messages = append(messages, llm.Message{
				Role:      "user",
				Content:   exploreWarnMessage(thrash.exploreStreak),
				Synthetic: true,
			})
		}
		// Mid-turn steering: host-supplied guidance injected before the next
		// LLM call, so the model course-corrects without a cancel/restart.
		if opt.Steer != nil {
			for _, s := range opt.Steer() {
				if s = strings.TrimSpace(s); s != "" {
					messages = append(messages, llm.Message{Role: "user", Content: s, Synthetic: true})
				}
			}
		}
	}
	return Result{Messages: messages, Usage: usage}, fmt.Errorf(
		"%w: %d (raise --max-turns / policy.max_turns, or set 0 for unlimited; prompt again keeps history)",
		ErrMaxTurns, maxTurns)
}

// stallBarrenBatches is how many consecutive barren tool batches end the run
// with ErrStuck. Three, not two: two identical batches are a plausible retry
// (a flaky command, a re-read after a failed edit), three is a loop. This is a
// package-level backstop, not a tuning knob — a run that legitimately needs
// more turns should raise max_turns, not loosen the stall floor.
const stallBarrenBatches = 3

// evidenceSet is the loop's novelty signal. The goal pack has a real evidence
// ledger; the plain loop does not, so "new evidence" here means "a (tool, args,
// result) triple this run has not produced before".
//
// Keying on the call as well as the result is what keeps the signal honest:
//   - the same tool+args returning changed output is progress (a poll watching
//     a file, a test rerun that now fails differently) — novel, never stalls;
//   - different tools that happen to return the same string (two empty greps,
//     two "ok" runs) are still distinct evidence — novel;
//   - only re-running the identical call for the identical result is barren.
//
// Results are hashed in full rather than by prefix: tool output routinely
// shares a long head (file banners, "=== RUN" preambles, identical grep
// context) and prefix keys made unrelated results collide into false stalls.
// Content is already bounded upstream by policy.max_tool_result_chars, so the
// hash cost is bounded too, and the digest keeps the set small for long runs.
type evidenceSet struct {
	seen map[string]struct{}
}

func newEvidenceSet() *evidenceSet {
	return &evidenceSet{seen: map[string]struct{}{}}
}

// note records one batch's (call, result) evidence and reports whether any of
// it was new. Empty batches and empty results count as no new evidence.
func (e *evidenceSet) note(calls []llm.ToolCall, msgs []llm.Message) bool {
	// Tool call ids are unique per call, so they can only identify which call
	// a result belongs to — never form part of the novelty key.
	callFP := make(map[string]string, len(calls))
	for _, tc := range calls {
		name := tc.Function.Name
		callFP[tc.ID] = name + "=" + normalizeArgsFP(name, json.RawMessage(tc.Function.Arguments))
	}
	fresh := false
	for _, m := range msgs {
		body := strings.TrimSpace(m.Content)
		if body == "" {
			continue
		}
		sum := sha256.Sum256([]byte(body))
		key := callFP[m.ToolCallID] + "\x00" + hex.EncodeToString(sum[:])
		if _, ok := e.seen[key]; !ok {
			e.seen[key] = struct{}{}
			fresh = true
		}
	}
	return fresh
}

// toolCallFingerprint is a stable key for stall detection (name + args per call).
// Bash args are normalized (cd prefixes collapsed) so status thrash collides.
func toolCallFingerprint(calls []llm.ToolCall) string {
	if len(calls) == 0 {
		return ""
	}
	var b strings.Builder
	for i, tc := range calls {
		if i > 0 {
			b.WriteByte('|')
		}
		name := tc.Function.Name
		b.WriteString(name)
		b.WriteByte('=')
		b.WriteString(normalizeArgsFP(name, json.RawMessage(tc.Function.Arguments)))
	}
	return b.String()
}

// toolSlot is one resolved call in a batch (soft result, hard error, or ErrDone).
type toolSlot struct {
	msg  llm.Message
	ok   bool // soft result ready to append
	hard error
	done bool // tool returned ErrDone — end loop after batch
}

func parallelLimit(opt Options) int {
	if opt.MaxParallelTools > 0 {
		return opt.MaxParallelTools
	}
	return DefaultMaxParallelTools
}

// runToolBatch executes all tool calls for one assistant turn.
// Soft results are returned in call order. The first hard error cancels
// siblings (fail-fast); finished soft results still append.
func runToolBatch(ctx context.Context, calls []llm.ToolCall, byName map[string]Tool, opt Options) ([]llm.Message, error) {
	if len(calls) == 0 {
		return nil, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	limit := parallelLimit(opt)
	if len(calls) == 1 || limit == 1 {
		return runToolBatchSequential(ctx, calls, byName, opt)
	}
	return runToolBatchParallel(ctx, calls, byName, opt, limit)
}

func runToolBatchSequential(ctx context.Context, calls []llm.ToolCall, byName map[string]Tool, opt Options) ([]llm.Message, error) {
	var out []llm.Message
	var done bool
	for _, tc := range calls {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		slot := execOneTool(ctx, tc, byName, opt)
		if slot.ok {
			out = append(out, slot.msg)
		}
		if slot.done {
			done = true
		}
		if slot.hard != nil {
			return out, slot.hard
		}
	}
	if done {
		return out, ErrDone
	}
	return out, nil
}

func runToolBatchParallel(ctx context.Context, calls []llm.ToolCall, byName map[string]Tool, opt Options, limit int) ([]llm.Message, error) {
	batchCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	slots := make([]toolSlot, len(calls))
	sem := make(chan struct{}, limit)
	var wg sync.WaitGroup
	var hardMu sync.Mutex
	var hardErr error

	for i, tc := range calls {
		i, tc := i, tc
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-batchCtx.Done():
				slots[i].hard = batchCtx.Err()
				return
			}
			if err := batchCtx.Err(); err != nil {
				slots[i].hard = err
				return
			}
			slot := execOneTool(batchCtx, tc, byName, opt)
			slots[i] = slot
			if slot.hard != nil {
				hardMu.Lock()
				if hardErr == nil {
					hardErr = slot.hard
				}
				hardMu.Unlock()
				cancel() // fail-fast: stop siblings
			}
		}()
	}
	wg.Wait()

	var out []llm.Message
	var done bool
	for i := range slots {
		if slots[i].ok {
			out = append(out, slots[i].msg)
		}
		if slots[i].done {
			done = true
		}
	}
	if hardErr != nil {
		return out, hardErr
	}
	if err := ctx.Err(); err != nil {
		return out, err
	}
	if done {
		return out, ErrDone
	}
	return out, nil
}

// execOneTool resolves allow/unknown and runs hooks+Exec for one call.
func execOneTool(ctx context.Context, tc llm.ToolCall, byName map[string]Tool, opt Options) toolSlot {
	name := tc.Function.Name
	if opt.AllowTool != nil {
		if err := opt.AllowTool(name); err != nil {
			return toolSlot{
				ok: true,
				msg: llm.Message{
					Role:       "tool",
					ToolCallID: tc.ID,
					Name:       name,
					Content:    "error: " + err.Error(),
				},
			}
		}
	}
	tool, ok := byName[name]
	if !ok {
		return toolSlot{
			ok: true,
			msg: llm.Message{
				Role:       "tool",
				ToolCallID: tc.ID,
				Name:       name,
				Content:    fmt.Sprintf("error: unknown tool %q", name),
			},
		}
	}
	args := json.RawMessage(tc.Function.Arguments)
	if len(args) == 0 {
		args = json.RawMessage(`{}`)
	}
	// Re-read short-circuit: do not re-dump the same file into context
	// (read tool and bash cat/sed/head/tail of already-seen paths).
	if name == "read" {
		if stub, ok := opt.thrash.maybeDedupeRead(args); ok {
			return toolSlot{
				ok: true,
				msg: llm.Message{
					Role:       "tool",
					ToolCallID: tc.ID,
					Name:       name,
					Content:    stub,
				},
			}
		}
	}
	if name == "bash" {
		if stub, ok := opt.thrash.maybeDedupeBash(args); ok {
			return toolSlot{
				ok: true,
				msg: llm.Message{
					Role:       "tool",
					ToolCallID: tc.ID,
					Name:       name,
					Content:    stub,
				},
			}
		}
	}
	out, err := runTool(ctx, tool, name, tc.ID, args, opt.Hooks)
	if err != nil && !errors.Is(err, ErrDone) {
		return toolSlot{hard: err}
	}
	out = TruncateToolResult(out, toolResultLimit(opt))
	out = opt.thrash.annotateRepeat(name, args, out)
	return toolSlot{
		ok:   true,
		done: errors.Is(err, ErrDone),
		msg: llm.Message{
			Role:       "tool",
			ToolCallID: tc.ID,
			Name:       name,
			Content:    out,
		},
	}
}

func toolResultLimit(opt Options) int {
	if opt.MaxToolResultChars > 0 {
		return opt.MaxToolResultChars
	}
	return DefaultMaxToolResultChars
}

func applyCompact(ctx context.Context, messages []llm.Message, opt Options, calib *ratioCalibrator) ([]llm.Message, error) {
	toolLim := toolResultLimit(opt)
	if opt.MaxContextChars <= 0 {
		return trimAllToolResults(messages, toolLim, toolLim/2), nil
	}
	// Estimate in "budget chars": raw chars rescaled by the calibrated
	// chars/token ratio, so a code-heavy history (which tokenizes denser than
	// the 4 chars/token heuristic) compacts before it blows the real window.
	ratio := calib.Ratio()
	raw := EstChars(messages)
	est := budgetChars(raw, ratio)
	if est <= opt.MaxContextChars {
		return trimAllToolResults(messages, toolLim, toolLim/2), nil
	}
	summary := ""
	for _, h := range opt.Hooks.PreCompact {
		if h == nil {
			continue
		}
		d, err := h(ctx, PreCompactEvent{
			EstChars:      est,
			MaxChars:      opt.MaxContextChars,
			CharsPerToken: ratio,
			Messages:      messages,
		})
		if err != nil {
			return nil, err
		}
		if d.Skip {
			return messages, nil
		}
		if d.Summary != "" {
			summary = d.Summary
		}
	}
	result := CompactTiered(messages, CompactTarget(opt.MaxContextChars, ratio), summary, toolLim)
	if result.CharsSaved > 0 || result.OverBudget {
		for _, h := range opt.Hooks.AfterCompact {
			if h != nil {
				h(ctx, AfterCompactEvent{
					Layer: result.Layer, CharsBefore: result.CharsBefore, CharsAfter: result.CharsAfter,
					CharsSaved: result.CharsSaved, MessagesBefore: result.MessagesBefore,
					MessagesAfter: result.MessagesAfter, OverBudget: result.OverBudget,
				})
			}
		}
	}
	return result.Messages, nil
}

// runTool applies PreTool → Exec (or deny) → PostTool and returns the model-visible result.
// A non-nil error aborts the whole agent Run (hook hard-fail or parent ctx done).
// Tool timeouts that leave parent ctx alive stay soft (model-visible error string).
func runTool(ctx context.Context, tool Tool, name, callID string, args json.RawMessage, hooks Hooks) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	start := time.Now()

	var extra string
	denied := false
	denyMsg := ""

	for _, h := range hooks.PreTool {
		if h == nil {
			continue
		}
		d, err := h(ctx, PreToolEvent{Name: name, Args: args, ToolCallID: callID})
		if err != nil {
			return "", err
		}
		if d.RewriteArgs && len(d.Args) > 0 {
			args = d.Args
		}
		if d.AdditionalContext != "" {
			if extra != "" {
				extra += "\n"
			}
			extra += d.AdditionalContext
		}
		if d.Deny {
			denied = true
			if d.Message != "" {
				denyMsg = d.Message
			} else {
				denyMsg = "denied by hook"
			}
			// Keep walking remaining hooks so later ones can still rewrite / annotate;
			// first deny sticks unless a later deny supplies a clearer Message.
		}
	}

	var out string
	var execErr error
	if denied {
		out = "error: " + denyMsg
	} else {
		out, execErr = tool.Exec(ctx, args)
		// Parent cancelled/deadline: hard-abort (do not soft-wrap and continue the batch).
		// Child-only timeouts (e.g. bash 60s) leave ctx alive → still soft below.
		if err := ctx.Err(); err != nil {
			return "", err
		}
		// ErrDone is a clean stop request — keep the result text, do not soft-wrap.
		if execErr != nil && !errors.Is(execErr, ErrDone) {
			out = "error: " + execErr.Error()
			execErr = nil
		}
	}

	if extra != "" {
		out = extra + "\n" + out
	}

	dur := time.Since(start)
	for _, h := range hooks.PostTool {
		if h == nil {
			continue
		}
		d, err := h(ctx, PostToolEvent{
			Name:       name,
			Args:       args,
			ToolCallID: callID,
			Result:     out,
			Denied:     denied,
			ExecErr:    execErr,
			Duration:   dur,
		})
		if err != nil {
			return "", err
		}
		if d.Rewrite {
			out = d.Result
		}
	}
	if errors.Is(execErr, ErrDone) {
		return out, ErrDone
	}
	return out, nil
}
