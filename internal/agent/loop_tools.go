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
	out, err := runTool(ctx, tool, name, tc.ID, args, opt.Hooks, func(s string) string {
		s = TruncateToolResult(s, toolResultLimit(opt))
		return frameUntrustedResult(tool, name, s, opt.UntrustedNonce)
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

// frameUntrustedResult wraps external tool bodies when the tool opts in via
// UntrustedSource (or is a known external builtin name).
func frameUntrustedResult(tool Tool, name, out, nonce string) string {
	if out == "" {
		return out
	}
	if u, ok := tool.(UntrustedSource); ok && u.Untrusted() {
		return WrapUntrusted(nonce, name, out)
	}
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "bash", "acp_delegate":
		return WrapUntrusted(nonce, name, out)
	}
	return out
}

func toolResultLimit(opt Options) int {
	if opt.MaxToolResultChars > 0 {
		return opt.MaxToolResultChars
	}
	return DefaultMaxToolResultChars
}

// applyCompact returns the message list to send for one LLM call, plus
// compacted = true when history was actually compacted (drop/snip tier ran,
// not just the under-budget tool trim). The soft budget from opt is hard-
// capped at MaxContextCharsHardCap regardless of ratio or explicit config.
func applyCompact(ctx context.Context, messages []llm.Message, opt Options, calib *ratioCalibrator) ([]llm.Message, bool, error) {
	toolLim := toolResultLimit(opt)
	if opt.MaxContextChars <= 0 {
		return trimAllToolResults(messages, toolLim, toolLim/2), false, nil
	}
	// Absolute ceiling: never let the soft budget exceed the hard cap. This
	// applies even to an explicit max_context_chars above the cap — a huge
	// window/config must not grow history past ~400k tokens.
	budget := opt.MaxContextChars
	if budget > MaxContextCharsHardCap {
		budget = MaxContextCharsHardCap
	}
	// Estimate in "budget chars": raw chars rescaled by the calibrated
	// chars/token ratio, so a code-heavy history (which tokenizes denser than
	// the 4 chars/token heuristic) compacts before it blows the real window.
	ratio := calib.Ratio()
	raw := EstChars(messages)
	est := budgetChars(raw, ratio)
	if est <= budget {
		return trimAllToolResults(messages, toolLim, toolLim/2), false, nil
	}
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
			return messages, false, nil
		}
		if d.Summary != "" {
			summary = d.Summary
		}
	}
	result := CompactTiered(messages, CompactTarget(CompactResumeBudget(budget), ratio), summary, toolLim)
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
	if errors.Is(execErr, ErrDone) {
		return out, ErrDone
	}
	return out, nil
}
