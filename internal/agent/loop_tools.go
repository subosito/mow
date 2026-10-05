package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/subosito/mow/internal/llm"
)

// maxToolCallsPerTurn bounds one model response. Thousands of calls can exhaust
// memory/IO and make the session unreplayable; normal parallel work is far below it.
const maxToolCallsPerTurn = 64

func runToolBatch(ctx context.Context, calls []llm.ToolCall, byName map[string]Tool, opt Options) ([]llm.Message, error) {
	if len(calls) == 0 {
		return nil, nil
	}
	if len(calls) > maxToolCallsPerTurn {
		return repairToolResults(calls, nil, fmt.Errorf("tool batch has %d calls; maximum is %d", len(calls), maxToolCallsPerTurn)), nil
	}
	for _, tc := range calls {
		if strings.TrimSpace(tc.ID) == "" || strings.TrimSpace(tc.Function.Name) == "" {
			return repairToolResults(calls, nil, errors.New("tool batch contains a call without id or function name")), nil
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// Interactive/sequential tools cannot safely run beside siblings whose
	// execution assumes the answer. Refuse the batch softly; the model can
	// reissue ask_user alone, then continue with the answer in history.
	for _, tc := range calls {
		if tool := byName[tc.Function.Name]; tool != nil {
			if seq, ok := tool.(interface{ Sequential() bool }); ok && seq.Sequential() && len(calls) > 1 {
				return sequentialBatchErrors(calls, tc.ID, tc.Function.Name), nil
			}
		}
	}

	limit := parallelLimit(opt)
	for _, tc := range calls {
		if tool := byName[tc.Function.Name]; tool != nil {
			if capped, ok := tool.(interface{ MaxParallel() int }); ok && capped.MaxParallel() > 0 {
				limit = min(limit, capped.MaxParallel())
			}
		}
	}
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
	appendPostBatchContext(calls, byName, out)
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
		wg.Go(func() {
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
		})
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
	appendPostBatchContext(calls, byName, out)
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
			tool := byName[name]
			if tool == nil {
				tool = softErrorTool{name: name}
			}
			return runPolicyDenied(ctx, tool, tc, opt, err)
		}
	}
	tool, ok := byName[name]
	if !ok {
		return runUnknownTool(ctx, tc, opt)
	}
	args := json.RawMessage(tc.Function.Arguments)
	if len(args) == 0 {
		args = json.RawMessage(`{}`)
	}
	out, err := runTool(ctx, tool, name, tc.ID, args, opt.Hooks, func(s string) string {
		// The cap applies to what enters history. A framed body pays for its
		// own markers, so the framed result never exceeds the limit and a
		// later tail trim cannot cut off the closing tag.
		lim := toolResultLimit(opt)
		framed := untrustedSource(tool, name)
		if framed {
			lim = max(lim-len(WrapUntrusted(opt.UntrustedNonce, name, ""))-len(truncatedMarker)-1, 1)
		}
		if len(s) > lim {
			if path := spillToolResult(opt.SpillDir, name, s); path != "" {
				s = TruncateToolResultSpilled(s, lim, path)
			} else {
				s = TruncateToolResult(s, lim)
			}
		}
		if framed && s != "" {
			return WrapUntrusted(opt.UntrustedNonce, name, s)
		}
		return s
	})
	if err != nil && !errors.Is(err, ErrDone) {
		return toolSlot{hard: err}
	}
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

// untrustedSource reports whether name's results are external content that
// must be framed before entering history.
func untrustedSource(tool Tool, name string) bool {
	if u, ok := tool.(UntrustedSource); ok && u.Untrusted() {
		return true
	}
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "bash", "delegate":
		return true
	}
	return false
}

func toolResultLimit(opt Options) int {
	if opt.MaxToolResultChars > 0 {
		return opt.MaxToolResultChars
	}
	return DefaultMaxToolResultChars
}

// applyCompact returns the message list to send for one LLM call, plus
// compacted = true when history was actually compacted (drop/snip tier ran,
// not just the under-budget tool trim).
//
// Two tripwires:
//   - MaxContextChars > 0: explicit char budget (tests), hard-capped.
//   - MaxContextChars == 0 && ContextWindow > 0: 80% of the window in tokens
//     (same units as the host ctx chip). Occupancy is last billed input or
//     EstChars / calibrator, whichever is higher.
//   - else: tool-result trim only (unknown window does not invent a char floor).
func applyCompact(ctx context.Context, messages []llm.Message, opt Options, calib *ratioCalibrator) ([]llm.Message, bool, error) {
	toolLim := toolResultLimit(opt)
	if opt.MaxContextChars < 0 {
		return capToolResults(messages, toolLim), false, nil
	}
	ratio := calib.Ratio()
	raw := EstChars(messages)
	var est, budget, target int
	switch {
	case opt.MaxContextChars > 0:
		budget = min(opt.MaxContextChars, MaxContextCharsHardCap)
		est = budgetChars(raw, ratio)
		if est <= budget {
			return capToolResults(messages, toolLim), false, nil
		}
		target = CompactTarget(CompactResumeBudget(budget), ratio)
	case opt.ContextWindow > 0 || opt.CompactAtTokens > 0:
		used := estimateUsedTokens(raw+opt.ToolSchemaChars, opt.LastInputTokens, ratio)
		budgetTok := EffectiveCompactTokens(opt.ContextWindow, opt.CompactAtTokens)
		if used < budgetTok {
			return capToolResults(messages, toolLim), false, nil
		}
		est = raw
		budget = int(float64(budgetTok) * clampRatio(ratio))
		resumeTok := CompactResumeBudget(budgetTok)
		target = max(int(float64(resumeTok)*clampRatio(ratio)), 1)
	default:
		return capToolResults(messages, toolLim), false, nil
	}
	// Probe first: CompactTiered is pure and cheap; PreCompact is not (hosts
	// archive the history and may spend an LLM call on a summary). A history
	// already at its floor — common while stale occupancy keeps tripping the
	// budget turn after turn — must not pay that price for a no-op.
	result := CompactTiered(messages, target, "", toolLim)
	if result.CharsSaved > 0 {
		summary := ""
		for _, h := range opt.Hooks.PreCompact {
			if h == nil {
				continue
			}
			d, err := h(ctx, PreCompactEvent{
				EstChars:      est,
				MaxChars:      budget,
				CharsPerToken: ratio,
				Messages:      messages,
			})
			if err != nil {
				return nil, false, err
			}
			if d.Skip {
				return capToolResults(messages, toolLim), false, nil
			}
			if d.Summary != "" {
				summary = d.Summary
			}
		}
		if summary != "" {
			result = CompactTiered(messages, target, summary, toolLim)
		}
	}
	// Always notify, including no-ops: PreCompact already advertised start
	// (TUI "auto-compact…"). Skipping AfterCompact when CharsSaved==0 left
	// that chrome stuck for the rest of the turn — common when occupancy
	// is still over the tripwire from LastInputTokens but history was
	// already compacted.
	for _, h := range opt.Hooks.AfterCompact {
		if h != nil {
			h(ctx, AfterCompactEvent{
				Layer: result.Layer, CharsBefore: result.CharsBefore, CharsAfter: result.CharsAfter,
				CharsSaved: result.CharsSaved, MessagesBefore: result.MessagesBefore,
				MessagesAfter: result.MessagesAfter, OverBudget: result.OverBudget,
			})
		}
	}
	return result.Messages, true, nil
}

// runTool applies PreTool → Exec (or deny) → PostTool and returns the model-visible result.
// A non-nil error aborts the whole agent Run (hook hard-fail or parent ctx done).
// Tool timeouts that leave parent ctx alive stay soft (model-visible error string).
func runTool(ctx context.Context, tool Tool, name, callID string, args json.RawMessage, hooks Hooks, finalize func(string) string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	start := time.Now()

	var extra string
	denied, halt := false, false
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
		if d.Halt {
			halt = true
			d.Deny = true
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
		}
	}

	if extra != "" {
		out = extra + "\n" + out
	}

	// Finalize (truncate + untrusted framing) BEFORE post-tool hooks so a
	// hook that annotates or caps a body operates on exactly what the model
	// will see — and its note lands outside the untrusted wrapper, not inside
	// it. Denied calls skip this: the deny text is ours, not tool output.
	if !denied && finalize != nil {
		out = finalize(out)
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
	if errors.Is(execErr, ErrDone) || halt {
		return out, ErrDone
	}
	return out, nil
}
