package engine

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/subosito/mow/internal/agent"
	"github.com/subosito/mow/internal/contextload"
	"github.com/subosito/mow/internal/llm"
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
	if err := e.ensureLLM(); err != nil {
		return RunResult{}, err
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return RunResult{}, fmt.Errorf("mow: empty prompt")
	}

	e.promptMu.Lock()
	defer e.promptMu.Unlock()

	e.mu.Lock()
	if !e.skillsLoaded {
		// Selector-on lazy load: prompt-matched skills + explicit skills.
		// Explicit skills (from config/CLI) load unconditionally here too,
		// merged with any prompt-matched skills so both appear in the prompt.
		var skills string
		sel := contextload.LoadSelectedSkills(e.skillDirs, text, e.skillSelect)
		if len(e.explicitSkills) > 0 {
			ex := contextload.LoadExplicitSkills(e.skillDirs, e.explicitSkills)
			skills = mergeSkillText(sel, ex)
		} else {
			skills = sel
		}
		e.skillsText = skills
		if skills != "" {
			e.sys = e.recomposeSystemLocked()
		}
		e.skillsLoaded = true
	}
	sys := e.sys
	sid := e.sid
	ws := ""
	model := ""
	var sysPrefix, sysPrefixModels []string
	if e.cfg != nil {
		ws = e.cfg.Workspace
		model = e.cfg.LLM.Model
		sysPrefix = e.cfg.LLM.SystemPrefix
		sysPrefixModels = e.cfg.LLM.SystemPrefixModels
	}
	if e.client != nil {
		if e.client.Model != "" {
			model = e.client.Model
		}
		// Live client holds the active prefix config (may match SetModel).
		if len(e.client.SystemPrefix) > 0 {
			sysPrefix = e.client.SystemPrefix
			sysPrefixModels = e.client.SystemPrefixModels
		}
	}
	// Identity only when no system_prefix applies — avoid dual "You are …".
	sys = contextload.WithOptionalIdentity(!llm.HasActiveSystemPrefix(sysPrefix, sysPrefixModels, model), sys)
	userPromptHooks := append([]UserPromptFunc(nil), e.life.onUserPrompt...)
	stopHooks := append([]StopFunc(nil), e.life.onStop...)
	sess := e.sess
	maxTurns := 0
	maxCtx := 0
	maxToolRes := 0
	maxPar := 0
	compactRatio := agent.DefaultCompactRatio
	if e.cfg != nil {
		maxTurns = e.cfg.Policy.MaxTurns
		maxCtx = e.cfg.Policy.MaxContextChars
		maxToolRes = e.cfg.Policy.MaxToolResultChars
		maxPar = e.cfg.Policy.MaxParallelTools
		if e.cfg.Policy.CompactRatio > 0 {
			compactRatio = e.cfg.Policy.CompactRatio
		}
	}
	if e.opt.MaxContextChars > 0 {
		maxCtx = e.opt.MaxContextChars
	} else {
		// Scale soft compaction budget from gateway context_window × ratio when
		// still on the default floor — otherwise 1M models compact absurdly early.
		// Use limitsLocked: e.mu is held; Limits() would re-lock and deadlock.
		maxCtx = resolveMaxContextChars(maxCtx, e.limitsLocked().ContextWindow, compactRatio)
	}
	if e.opt.MaxToolResultChars > 0 {
		maxToolRes = e.opt.MaxToolResultChars
	}
	chat := e.chat
	tools := append([]agent.Tool(nil), e.tools...)
	prior := e.prior
	hooks := e.hooks
	pol := e.pol
	cfg := e.cfg
	readOnlyExt := e.readOnlyExt
	e.mu.Unlock()

	// Per-call tools (e.g. goal_report only during goal.Runner steps).
	for _, t := range opt.ExtraTools {
		if t == nil {
			continue
		}
		tools = append(tools, adaptTool(t))
	}
	allowed := allowedToolSet(opt.AllowedTools)
	if len(allowed) > 0 {
		tools = filterAgentToolsByAllowed(tools, allowed)
	}

	// Cancellable run context + stable id for hosts/orchestrators.
	// Clear stale guidance BEFORE the run is observable: once beginRun marks
	// the engine busy (and EventRunStart fires), a host may legitimately steer,
	// and a later reset would wipe a steer aimed at this very turn.
	e.mu.Lock()
	e.steer = nil
	e.mu.Unlock()
	ctx, runID := e.beginRun(ctx)
	defer e.endRun()
	// Tools (e.g. delegate) can Emit via EngineFromContext without a stored pointer.
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

	if sess != nil && !opt.Ephemeral {
		// Persist the model at the turn boundary so resume follows the session's
		// last-used model rather than whatever default is active next launch.
		// Record the session default effort (before any auto-downshift) so a
		// short "thanks" turn does not stick medium into resume forever.
		if aerr := sess.Append(session.Event{Type: "runtime", Model: model, Wire: e.Wire(), Effort: e.Effort()}); aerr != nil {
			e.log().Warn("mow: session model append failed (resume may use default)", "err", aerr)
		}
		if aerr := sess.Append(session.Event{Type: "user", Role: "user", Content: text}); aerr != nil {
			e.log().Warn("mow: session append failed (resume history incomplete)", "err", aerr)
		}
	}

	// Downshift high/max effort for short mechanical prompts (restored after).
	restoreEffort := e.applyAutoEffort(text)
	defer restoreEffort()

	e.log().Debug("mow run start", "run_id", runID, "session_id", sid, "workspace", ws)
	e.Emit(Event{Type: EventRunStart, RunID: runID, SessionID: sid, Text: text, Model: model, Effort: e.requestEffort()})

	// Stream callbacks: fan-out to OnToken/OnReasoning and Event stream.
	e.onTokenMu.Lock()
	userTok := e.onToken
	userReason := e.onReasoning
	e.onTokenMu.Unlock()
	// Pre-first-byte signal: the loop registers the current call's
	// first-byte func on the engine (SetModelActive); the first streamed
	// delta — content, reasoning, or any upstream frame (OnActivity) — ends
	// the wait state before chat returns. Prompt is serialized by promptMu.
	defer func() {
		e.activeMu.Lock()
		e.modelActiveFn = nil
		e.activeMu.Unlock()
	}()
	onTok := func(delta string) {
		e.signalModelActive()
		if userTok != nil {
			userTok(delta)
		}
		e.Emit(Event{Type: EventToken, RunID: runID, SessionID: sid, Delta: delta})
	}
	onReason := func(delta string) {
		e.signalModelActive()
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

	// Retry reporting: the built-in client (and cooperative providers) fire
	// this once per scheduled retry, before the backoff sleep. During the
	// sleep the wait monitor's "gateway silent" copy would be a lie — the
	// gateway is not being asked — so the gate suppresses wait ticks until
	// the new attempt is in flight and the retry copy stays on screen.
	gate := &retryGate{}
	e.onTokenMu.Lock()
	prevRetry := e.onRetryFn
	e.onRetryFn = func(info RetryInfo) {
		gate.note(info.Delay)
		e.Emit(Event{Type: EventModelRetry, RunID: runID, SessionID: sid,
			Model: model, Effort: e.requestEffort(), Delta: retryCopy(info)})
	}
	e.onTokenMu.Unlock()
	defer func() {
		e.onTokenMu.Lock()
		e.onRetryFn = prevRetry
		e.onTokenMu.Unlock()
	}()

	// Inject tool lifecycle events as outer hooks (do not mutate e.hooks permanently).
	hooks = hooksWithEvents(hooks, e, runID, sid)

	// Mid-turn steer: the loop registers the current LLM call's cancel func
	// here; Engine.Steer calls it to interrupt ONLY the in-flight call (the
	// run ctx stays alive), and the loop reissues with the steer appended.
	defer func() {
		e.mu.Lock()
		e.steerCancel = nil
		e.mu.Unlock()
	}()

	res, err := agent.Run(ctx, chat, text, agent.Options{
		System:             sys,
		MaxTurns:           maxTurns,
		Tools:              tools,
		PriorMessages:      prior,
		Hooks:              hooks,
		OnToken:            onTok,
		MaxContextChars:    maxCtx,
		MaxToolResultChars: maxToolRes,
		MaxOutputTokens:    e.maxOutputTokens(),
		OnPrefixDrift:      e.prefixDriftReporter(),
		MaxParallelTools:   maxPar,
		Workspace:          ws,
		UntrustedNonce:     e.untrustedNonce,
		Steer:              e.drainSteer,
		SetLLMCancel: func(cancel context.CancelFunc) {
			e.mu.Lock()
			e.steerCancel = cancel
			e.mu.Unlock()
		},
		OnModelWait: func(elapsed time.Duration) {
			delta, suppress := gate.waitCopy(elapsed)
			if suppress {
				return
			}
			e.Emit(Event{Type: EventModelWait, RunID: runID, SessionID: sid,
				Model: model, Effort: e.requestEffort(), Delta: delta})
		},
		OnModelActive: func() {
			e.Emit(Event{Type: EventModelActive, RunID: runID, SessionID: sid,
				Model: model, Effort: e.requestEffort()})
		},
		SetModelActive: func(signal func()) {
			e.activeMu.Lock()
			e.modelActiveFn = signal
			e.activeMu.Unlock()
		},
		AllowTool: func(name string) error {
			if !isAllowedTool(name, allowed) {
				return fmt.Errorf("tool %q denied: not in allowed tool set", name)
			}
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

	// Ephemeral asides run against current context but leave no trace: skip the
	// history/transcript update and the session append, so the exchange never
	// re-enters a later prompt.
	if !opt.Ephemeral {
		e.mu.Lock()
		if len(res.Messages) > 0 {
			e.prior = res.Messages
		}
		// Keep in-memory transcript aligned with what we append to the session file.
		e.transcript = append(e.transcript, Message{Role: "user", Content: text, Timestamp: time.Now().UTC()})
		if strings.TrimSpace(res.Text) != "" {
			e.transcript = append(e.transcript, Message{Role: "assistant", Content: res.Text, Timestamp: time.Now().UTC()})
		}
		e.mu.Unlock()

		if sess != nil {
			var aerr error
			if res.Text != "" {
				aerr = sess.Append(session.Event{Type: "assistant", Role: "assistant", Content: res.Text})
			}
			// One complete replay snapshot per turn. Legacy per-message snapshots
			// remain readable, but rewriting every historical message here made
			// session files and append syscalls grow quadratically.
			if perr := sess.AppendSnapshot(res.Messages); perr != nil && aerr == nil {
				aerr = perr
			}
			if aerr != nil {
				e.log().Warn("mow: session append failed (resume history incomplete)", "err", aerr)
			}
		}
	}

	stop := stopReasonFrom(err)
	// Stall is its own signal: the loop gave up because consecutive tool
	// batches added no new evidence, not because the budget ran out.
	if stop == StopStuck {
		e.Emit(Event{
			Type: EventStall, RunID: runID, SessionID: sid,
			StopReason: stop, Text: errString(err),
		})
	}
	usage := Usage{InputTokens: res.Usage.InputTokens, OutputTokens: res.Usage.OutputTokens, CachedInputTokens: res.Usage.CachedInputTokens, CacheWriteInputTokens: res.Usage.CacheWriteInputTokens}
	out = RunResult{Text: res.Text, SessionID: sid, RunID: runID, StopReason: stop, Usage: usage}
	e.Emit(Event{
		Type: EventRunEnd, RunID: runID, SessionID: sid,
		Text: res.Text, StopReason: stop, Error: errString(err),
		InputTokens: usage.InputTokens, OutputTokens: usage.OutputTokens,
		CachedInputTokens:     usage.CachedInputTokens,
		CacheWriteInputTokens: usage.CacheWriteInputTokens,
		ProviderToolCalls:     res.Usage.ServerSideToolCalls,
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

// retryGate tracks one run's backoff state so wait ticks stay honest: while
// a retry sleep is in flight the gateway is not being asked, so the
// "waiting for first response" copy would be misleading during backoff.
type retryGate struct {
	mu    sync.Mutex
	until time.Time        // backoff sleep end ≈ next attempt start
	now   func() time.Time // test hook; nil = time.Now
}

func (g *retryGate) timeNow() time.Time {
	if g.now != nil {
		return g.now()
	}
	return time.Now()
}

// note records a scheduled retry: the backoff sleep runs until now+delay.
func (g *retryGate) note(delay time.Duration) {
	g.mu.Lock()
	g.until = g.timeNow().Add(delay)
	g.mu.Unlock()
}

// waitCopy maps a monitor tick to honest copy. suppress=true means a backoff
// sleep is in flight: emit nothing and keep the retry copy on screen. After
// the sleep, silence is measured from the retry's end — approximately the
// new attempt's start — not from the original request. A tick at elapsed 0
// marks a new call and clears any prior call's marker.
func (g *retryGate) waitCopy(elapsed time.Duration) (copy string, suppress bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if elapsed == 0 {
		g.until = time.Time{}
		return "request sent; waiting for response", false
	}
	if g.until.IsZero() {
		return "waiting for first response", false
	}
	now := g.timeNow()
	if now.Before(g.until) {
		return "", true
	}
	return "waiting for first response", false
}

// retryCopy renders a scheduled retry as honest status copy. The status code
// is the most it names — never URLs, headers, or bodies.
func retryCopy(info RetryInfo) string {
	d := info.Delay.Round(time.Second)
	if d < time.Second {
		d = time.Second
	}
	switch info.Kind {
	case RetryUnavailable:
		return fmt.Sprintf("provider unavailable · reconnecting in %s", d)
	case RetryNetwork:
		return fmt.Sprintf("network error · retrying in %s", d)
	default:
		if info.Status > 0 {
			return fmt.Sprintf("provider busy · retrying in %s", d)
		}
		return fmt.Sprintf("provider busy · retrying in %s", d)
	}
}

func hooksWithEvents(h agent.Hooks, e *Engine, runID, sid string) agent.Hooks {
	// Copy slices so we do not mutate engine state.
	pre := append([]agent.PreToolFunc(nil), h.PreTool...)
	post := append([]agent.PostToolFunc(nil), h.PostTool...)
	compact := append([]agent.AfterCompactFunc(nil), h.AfterCompact...)
	after := append([]agent.AfterTurnFunc(nil), h.AfterTurn...)
	pre = append([]agent.PreToolFunc{func(ctx context.Context, ev agent.PreToolEvent) (agent.PreToolDecision, error) {
		e.Emit(Event{
			Type: EventToolStart, RunID: runID, SessionID: sid,
			Tool: ev.Name, ToolCallID: ev.ToolCallID, Args: ev.Args,
		})
		e.log().Debug("mow tool start", "run_id", runID, "tool", ev.Name, "tool_call_id", ev.ToolCallID)
		return agent.PreToolDecision{}, nil
	}}, pre...)
	// Order: event emitter first (captures the full result for EventToolEnd),
	// then ext + Options PostTool hooks in registration order. Packs that
	// rewrite results for history register as ordinary ext hooks — there is
	// no special tail slot.
	post = append([]agent.PostToolFunc{func(ctx context.Context, ev agent.PostToolEvent) (agent.PostToolDecision, error) {
		res := ev.Result
		const max = 4000
		if len(res) > max {
			res = res[:max] + "…(truncated)"
		}
		errStr := ""
		// ErrDone is a clean stop from tools like goal_report — not a failure.
		// Emitting it as Error made the CLI print "✗ goal_report: agent: done".
		if ev.ExecErr != nil && !errors.Is(ev.ExecErr, agent.ErrDone) {
			errStr = ev.ExecErr.Error()
		} else if ev.Denied && errStr == "" {
			// PreTool denials put the reason in Result as "error: <msg>"
			// and leave ExecErr nil. Hosts (mowi) paint Denied + empty
			// Error as a bare "denied", which hides the real reason.
			errStr = strings.TrimPrefix(ev.Result, "error: ")
			if errStr == ev.Result {
				errStr = strings.TrimSpace(ev.Result)
			}
			if errStr == "" {
				errStr = "denied by hook"
			}
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
	compact = append([]agent.AfterCompactFunc{func(ctx context.Context, ev agent.AfterCompactEvent) {
		// Keep ContextTokens() in step with the compacted projection so hosts
		// (header ctx%) do not wait for the next LLM usage report.
		e.mu.Lock()
		cpt := 0.0
		if e.lastProviderTokens > 0 && ev.CharsBefore > 0 {
			cpt = float64(ev.CharsBefore) / float64(e.lastProviderTokens)
		}
		e.lastCtxEstimate = estimateCtxTokens(ev.CharsAfter, cpt)
		e.mu.Unlock()
		e.Emit(Event{
			Type: EventCompact, RunID: runID, SessionID: sid, Layer: CompactLayer(ev.Layer),
			CharsBefore: ev.CharsBefore, CharsAfter: ev.CharsAfter, CharsSaved: ev.CharsSaved,
			MessagesBefore: ev.MessagesBefore, MessagesAfter: ev.MessagesAfter, OverBudget: ev.OverBudget,
			Auto: true,
		})
	}}, compact...)
	after = append([]agent.AfterTurnFunc{func(ctx context.Context, ev agent.AfterTurnEvent) {
		e.Emit(Event{
			Type: EventTurn, RunID: runID, SessionID: sid,
			Text: ev.AssistantText, HasToolCalls: ev.HasToolCalls,
		})
	}}, after...)
	h.PreTool = pre
	h.PostTool = post
	// Archive pre-compact history for hosts/review.
	preC := append([]agent.PreCompactFunc(nil), h.PreCompact...)
	preC = append(preC, func(ctx context.Context, ev agent.PreCompactEvent) (agent.PreCompactDecision, error) {
		// Tell hosts a compact is starting before archive / optional summary
		// so the TUI can show "auto-compact…" even when the work is instant.
		e.Emit(Event{Type: EventCompactStart, RunID: runID, SessionID: sid, Auto: true})
		if e.sess != nil && len(ev.Messages) > 0 {
			if _, err := e.sess.ArchiveCompact(ev.Messages, "auto", 0); err != nil {
				e.log().Warn("context archive", "err", err)
			}
		}
		// Opt-in LLM summary. Runs last so a host-supplied PreCompact hook
		// still wins: an explicit Summary from the host is not overwritten.
		if e.cfg.Policy.CompactSummary || e.opt.CompactSummary {
			if s := e.summarizeHistory(ctx, ev); s != "" {
				return agent.PreCompactDecision{Summary: s}, nil
			}
		}
		return agent.PreCompactDecision{}, nil
	})
	h.PreCompact = preC
	h.AfterCompact = compact
	h.AfterTurn = after
	// Spend ceiling first in the PreModel chain: a run that is out of budget
	// must not reach any other gate's side effects.
	// A gate error here is impossible: mow.New already refused to construct an
	// Engine with an unenforceable ceiling.
	if gate, gerr := e.budgetGate(); gerr == nil && gate != nil {
		h.PreModel = append([]agent.PreModelFunc{gate}, h.PreModel...)
	}
	return h
}
func stopReasonFrom(err error) string {
	if err == nil {
		return StopCompleted
	}
	if errors.Is(err, agent.ErrBudget) {
		return StopBudget
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return StopCancelled
	}
	if errors.Is(err, agent.ErrMaxTurns) {
		return StopMaxTurns
	}
	if errors.Is(err, agent.ErrStuck) {
		return StopStuck
	}
	if errors.Is(err, agent.ErrTruncated) {
		return StopTruncated
	}
	return StopError
}

// resolveMaxContextChars picks the soft history budget.
//   - cfgMax 0 → compaction off
//   - cfgMax == default (100k) and window known → scale from window × ratio
//     (default ratio 0.5 → 1M tokens ≈ 2M chars ≈ 500k tok-eq history, hard-capped at agent.MaxContextCharsHardCap)
//   - cfgMax explicit other value → respect absolute config
//   - no window → keep cfgMax
func resolveMaxContextChars(cfgMax, windowTokens int, ratio float64) int {
	if cfgMax <= 0 {
		return 0
	}
	if windowTokens <= 0 {
		return cfgMax
	}
	// Only auto-raise when still on the built-in default; explicit yaml wins.
	if cfgMax != agent.DefaultMaxContextChars {
		return cfgMax
	}
	scaled := agent.ContextCharsBudget(windowTokens, ratio)
	if scaled > cfgMax {
		return scaled
	}
	return cfgMax
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
