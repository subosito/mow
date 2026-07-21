package mow

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/subosito/mow/internal/agent"
	"github.com/subosito/mow/internal/session"
)

// Prompt runs one user turn (tools may multi-step internally).
func (e *Engine) Prompt(ctx context.Context, text string) (RunResult, error) {
	return e.PromptWith(ctx, text, PromptOpts{})
}

// PromptWith is Prompt with per-call options (e.g. SystemAppend).
func (e *Engine) PromptWith(ctx context.Context, text string, opt PromptOpts) (out RunResult, err error) {
	if e == nil {
		return RunResult{}, fmt.Errorf("mow: nil engine")
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return RunResult{}, fmt.Errorf("mow: empty prompt")
	}

	e.promptMu.Lock()
	defer e.promptMu.Unlock()

	e.mu.Lock()
	sys := e.sys
	sid := e.sid
	ws := ""
	if e.cfg != nil {
		ws = e.cfg.Workspace
	}
	userPromptHooks := append([]UserPromptFunc(nil), e.life.onUserPrompt...)
	stopHooks := append([]StopFunc(nil), e.life.onStop...)
	sess := e.sess
	maxTurns := 0
	maxCtx := 0
	maxToolRes := 0
	maxPar := 0
	if e.cfg != nil {
		maxTurns = e.cfg.Policy.MaxTurns
		maxCtx = e.cfg.Policy.MaxContextChars
		maxToolRes = e.cfg.Policy.MaxToolResultChars
		maxPar = e.cfg.Policy.MaxParallelTools
	}
	if e.opt.MaxContextChars > 0 {
		maxCtx = e.opt.MaxContextChars
	}
	if e.opt.MaxToolResultChars > 0 {
		maxToolRes = e.opt.MaxToolResultChars
	}
	chat := e.chat
	tools := e.tools
	prior := e.prior
	hooks := e.hooks
	pol := e.pol
	cfg := e.cfg
	readOnlyExt := e.readOnlyExt
	e.mu.Unlock()

	// Cancellable run context + stable id for hosts/orchestrators.
	ctx, runID := e.beginRun(ctx)
	defer e.endRun()
	// Tools (e.g. acp_delegate) can Emit via EngineFromContext without a stored pointer.
	ctx = ContextWithEngine(ctx, e)

	// Per-call system append (packs: goal protocol, etc.).
	if s := strings.TrimSpace(opt.SystemAppend); s != "" {
		if sys != "" {
			sys += "\n\n" + s
		} else {
			sys = s
		}
	}

	// UserPrompt hooks may rewrite text or append system for this call only.
	for _, fn := range userPromptHooks {
		if fn == nil {
			continue
		}
		d, herr := fn(ctx, UserPromptEvent{
			Text:      text,
			SessionID: sid,
			Workspace: ws,
		})
		if herr != nil {
			out = RunResult{SessionID: sid, RunID: runID, StopReason: StopError}
			e.Emit(Event{Type: EventRunEnd, RunID: runID, SessionID: sid, StopReason: StopError, Error: herr.Error()})
			return out, herr
		}
		if d.RewriteText {
			text = strings.TrimSpace(d.Text)
			if text == "" {
				err = fmt.Errorf("mow: empty prompt after UserPrompt hook")
				out = RunResult{SessionID: sid, RunID: runID, StopReason: StopError}
				e.Emit(Event{Type: EventRunEnd, RunID: runID, SessionID: sid, StopReason: StopError, Error: err.Error()})
				return out, err
			}
		}
		if s := strings.TrimSpace(d.SystemAppend); s != "" {
			if sys != "" {
				sys += "\n\n" + s
			} else {
				sys = s
			}
		}
	}

	if sess != nil {
		if aerr := sess.Append(session.Event{Type: "user", Role: "user", Content: text}); aerr != nil {
			e.log().Warn("mow: session append failed (resume history incomplete)", "err", aerr)
		}
	}

	e.log().Debug("mow run start", "run_id", runID, "session_id", sid, "workspace", ws)
	e.Emit(Event{Type: EventRunStart, RunID: runID, SessionID: sid, Text: text})

	// Stream callbacks: fan-out to OnToken/OnReasoning and Event stream.
	e.onTokenMu.Lock()
	userTok := e.onToken
	userReason := e.onReasoning
	e.onTokenMu.Unlock()
	onTok := func(delta string) {
		if userTok != nil {
			userTok(delta)
		}
		e.Emit(Event{Type: EventToken, RunID: runID, SessionID: sid, Delta: delta})
	}
	onReason := func(delta string) {
		if userReason != nil {
			userReason(delta)
		}
		e.Emit(Event{Type: EventReasoning, RunID: runID, SessionID: sid, Delta: delta})
	}
	// Temporarily install wrappers for the default LLM client path.
	e.SetOnToken(onTok)
	e.SetOnReasoning(onReason)
	defer func() {
		e.SetOnToken(userTok)
		e.SetOnReasoning(userReason)
	}()

	// Inject tool lifecycle events as outer hooks (do not mutate e.hooks permanently).
	hooks = hooksWithEvents(hooks, e, runID, sid)

	res, err := agent.Run(ctx, chat, text, agent.Options{
		System:             sys,
		MaxTurns:           maxTurns,
		Tools:              tools,
		PriorMessages:      prior,
		Hooks:              hooks,
		OnToken:            onTok,
		MaxContextChars:    maxCtx,
		MaxToolResultChars: maxToolRes,
		MaxParallelTools:   maxPar,
		AllowTool: func(name string) error {
			// Read-only prompts allow only known side-effect-free tools.
			// Ext/MCP tools are denied unless they declared ReadOnly() —
			// an editor "ask" session must not write through an extension.
			if opt.ReadOnly && !isReadOnlyTool(name, readOnlyExt) {
				return fmt.Errorf("tool %q denied: read-only prompt", name)
			}
			if isBuiltin(name) && cfg != nil && !cfg.ToolEnabled(name) {
				return fmt.Errorf("tool %q not enabled", name)
			}
			if pol != nil {
				return pol.AllowTool(name)
			}
			return nil
		},
	})

	e.mu.Lock()
	if len(res.Messages) > 0 {
		e.prior = res.Messages
	}
	// Keep in-memory transcript aligned with what we append to the session file.
	e.transcript = append(e.transcript, Message{Role: "user", Content: text})
	if strings.TrimSpace(res.Text) != "" {
		e.transcript = append(e.transcript, Message{Role: "assistant", Content: res.Text})
	}
	e.mu.Unlock()

	if sess != nil {
		var aerr error
		if res.Text != "" {
			aerr = sess.Append(session.Event{Type: "assistant", Role: "assistant", Content: res.Text})
		}
		// Full message dump for agent resume (LoadMessages keeps only the last snapshot).
		for _, m := range res.Messages {
			mm := m
			if perr := sess.Append(session.Event{Type: "message", Message: &mm}); perr != nil && aerr == nil {
				aerr = perr
			}
		}
		if aerr != nil {
			e.log().Warn("mow: session append failed (resume history incomplete)", "err", aerr)
		}
	}

	stop := stopReasonFrom(err)
	usage := Usage{InputTokens: res.Usage.InputTokens, OutputTokens: res.Usage.OutputTokens}
	out = RunResult{Text: res.Text, SessionID: sid, RunID: runID, StopReason: stop, Usage: usage}
	e.Emit(Event{
		Type: EventRunEnd, RunID: runID, SessionID: sid,
		Text: res.Text, StopReason: stop, Error: errString(err),
		InputTokens: usage.InputTokens, OutputTokens: usage.OutputTokens,
	})
	e.log().Debug("mow run end", "run_id", runID, "session_id", sid, "stop_reason", stop, "err", err)

	for _, fn := range stopHooks {
		if fn != nil {
			fn(ctx, StopEvent{Text: out.Text, Err: err, SessionID: sid})
		}
	}
	if err != nil {
		return out, err
	}
	return out, nil
}

func hooksWithEvents(h agent.Hooks, e *Engine, runID, sid string) agent.Hooks {
	// Copy slices so we do not mutate engine state.
	pre := append([]agent.PreToolFunc(nil), h.PreTool...)
	post := append([]agent.PostToolFunc(nil), h.PostTool...)
	after := append([]agent.AfterTurnFunc(nil), h.AfterTurn...)
	pre = append([]agent.PreToolFunc{func(ctx context.Context, ev agent.PreToolEvent) (agent.PreToolDecision, error) {
		e.Emit(Event{
			Type: EventToolStart, RunID: runID, SessionID: sid,
			Tool: ev.Name, ToolCallID: ev.ToolCallID, Args: ev.Args,
		})
		e.log().Debug("mow tool start", "run_id", runID, "tool", ev.Name, "tool_call_id", ev.ToolCallID)
		return agent.PreToolDecision{}, nil
	}}, pre...)
	post = append([]agent.PostToolFunc{func(ctx context.Context, ev agent.PostToolEvent) (agent.PostToolDecision, error) {
		res := ev.Result
		const max = 4000
		if len(res) > max {
			res = res[:max] + "…(truncated)"
		}
		errStr := ""
		if ev.ExecErr != nil {
			errStr = ev.ExecErr.Error()
		}
		durMs := ev.Duration.Milliseconds()
		e.Emit(Event{
			Type: EventToolEnd, RunID: runID, SessionID: sid,
			Tool: ev.Name, ToolCallID: ev.ToolCallID, Args: ev.Args,
			Result: res, Denied: ev.Denied, Error: errStr, DurationMs: durMs,
		})
		e.log().Debug("mow tool end", "run_id", runID, "tool", ev.Name, "denied", ev.Denied, "error", errStr, "duration_ms", durMs)
		return agent.PostToolDecision{}, nil
	}}, post...)
	after = append([]agent.AfterTurnFunc{func(ctx context.Context, ev agent.AfterTurnEvent) {
		e.Emit(Event{
			Type: EventTurn, RunID: runID, SessionID: sid,
			Text: ev.AssistantText, HasToolCalls: ev.HasToolCalls,
		})
	}}, after...)
	h.PreTool = pre
	h.PostTool = post
	h.AfterTurn = after
	return h
}

func stopReasonFrom(err error) string {
	if err == nil {
		return StopCompleted
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return StopCancelled
	}
	if errors.Is(err, agent.ErrMaxTurns) {
		return StopMaxTurns
	}
	return StopError
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
