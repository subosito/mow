// Package agent runs the tool-calling loop.
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

// ErrMaxTurns is returned when the agent loop hits Options.MaxTurns.
var ErrMaxTurns = errors.New("agent: max turns exceeded")

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
	System   string
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
}

// Result is the final assistant text and message history.
type Result struct {
	Text     string
	Messages []llm.Message
	// Usage is provider-reported tokens summed across every LLM call in the
	// run (zero when the provider sent none).
	Usage llm.Usage
}

// Run executes the agent loop until the model returns text without tool calls or max turns.
func Run(ctx context.Context, chat ChatFn, userPrompt string, opt Options) (Result, error) {
	if chat == nil {
		return Result{}, fmt.Errorf("agent: chat function required")
	}
	if strings.TrimSpace(userPrompt) == "" {
		return Result{}, fmt.Errorf("agent: empty prompt")
	}
	maxTurns := opt.MaxTurns
	if maxTurns <= 0 {
		maxTurns = 40
	}

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

	for turn := 0; turn < maxTurns; turn++ {
		if err := ctx.Err(); err != nil {
			return Result{Messages: messages, Usage: usage}, err
		}
		var specs []llm.ToolSpec
		if len(toolSpecs) > 0 {
			specs = toolSpecs
		}
		send, err := applyCompact(ctx, messages, opt)
		if err != nil {
			return Result{Messages: messages, Usage: usage}, err
		}
		msg, err := chat(ctx, send, specs)
		if err != nil {
			return Result{Messages: messages, Usage: usage}, err
		}
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
			return Result{Text: strings.TrimSpace(msg.Content), Messages: messages, Usage: usage}, nil
		}

		toolMsgs, err := runToolBatch(ctx, msg.ToolCalls, byName, opt)
		messages = append(messages, toolMsgs...)
		if err != nil {
			return Result{Messages: messages, Usage: usage}, err
		}
	}
	return Result{Messages: messages, Usage: usage}, fmt.Errorf("%w: %d", ErrMaxTurns, maxTurns)
}

// toolSlot is one resolved call in a batch (soft result or hard error).
type toolSlot struct {
	msg  llm.Message
	ok   bool // soft result ready to append
	hard error
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
	for _, tc := range calls {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		slot := execOneTool(ctx, tc, byName, opt)
		if slot.ok {
			out = append(out, slot.msg)
		}
		if slot.hard != nil {
			return out, slot.hard
		}
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
	for i := range slots {
		if slots[i].ok {
			out = append(out, slots[i].msg)
		}
	}
	if hardErr != nil {
		return out, hardErr
	}
	if err := ctx.Err(); err != nil {
		return out, err
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
	out, err := runTool(ctx, tool, name, tc.ID, args, opt.Hooks)
	if err != nil {
		return toolSlot{hard: err}
	}
	out = TruncateToolResult(out, toolResultLimit(opt))
	return toolSlot{
		ok: true,
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

func applyCompact(ctx context.Context, messages []llm.Message, opt Options) ([]llm.Message, error) {
	toolLim := toolResultLimit(opt)
	// Always trim oversized tool bodies before the LLM call (cheap, high impact).
	messages = trimAllToolResults(messages, toolLim, toolLim/2)

	if opt.MaxContextChars <= 0 {
		return messages, nil
	}
	est := estChars(messages)
	if est <= opt.MaxContextChars {
		return messages, nil
	}
	summary := ""
	for _, h := range opt.Hooks.PreCompact {
		if h == nil {
			continue
		}
		d, err := h(ctx, PreCompactEvent{EstChars: est, MaxChars: opt.MaxContextChars})
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
	return CompactOpts(messages, opt.MaxContextChars, summary, toolLim), nil
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
		if execErr != nil {
			out = "error: " + execErr.Error()
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
	return out, nil
}
