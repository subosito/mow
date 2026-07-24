package mow

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

// SetOnToken sets (or clears) the streaming content callback for subsequent LLM calls.
func (e *Engine) SetOnToken(fn func(delta string)) {
	if e == nil {
		return
	}
	e.onTokenMu.Lock()
	e.onToken = fn
	e.onTokenMu.Unlock()
}

// SetOnReasoning sets (or clears) the streaming reasoning/thinking callback.
// Reasoning is UI-only and is not part of Message.Content or session history.
func (e *Engine) SetOnReasoning(fn func(delta string)) {
	if e == nil {
		return
	}
	e.onTokenMu.Lock()
	e.onReasoning = fn
	e.onTokenMu.Unlock()
}

// SetOnEvent replaces all lifecycle event listeners with fn (or clears when nil).
// Prefer AddOnEvent when multiple consumers share one Engine (e.g. host + mow rpc).
func (e *Engine) SetOnEvent(fn EventFunc) {
	if e == nil {
		return
	}
	e.onTokenMu.Lock()
	e.onEvents = nil
	e.nextEventID = 0
	if fn != nil {
		e.nextEventID++
		e.onEvents = []eventSub{{id: e.nextEventID, fn: fn}}
	}
	e.onTokenMu.Unlock()
}

// AddOnEvent registers a lifecycle event listener. Returns unsubscribe (safe to call once).
// Listeners are invoked in registration order; must not block long.
func (e *Engine) AddOnEvent(fn EventFunc) (unsubscribe func()) {
	if e == nil || fn == nil {
		return func() {}
	}
	e.onTokenMu.Lock()
	e.nextEventID++
	id := e.nextEventID
	e.onEvents = append(e.onEvents, eventSub{id: id, fn: fn})
	e.onTokenMu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			e.onTokenMu.Lock()
			out := e.onEvents[:0]
			for _, s := range e.onEvents {
				if s.id != id {
					out = append(out, s)
				}
			}
			e.onEvents = out
			e.onTokenMu.Unlock()
		})
	}
}

// log returns the Options.Logger or the process default. Engine logging goes
// through here so an embedder can capture it without touching slog global.
func (e *Engine) log() *slog.Logger {
	if e != nil && e.logger != nil {
		return e.logger
	}
	return slog.Default()
}

// Cancel aborts the in-flight Prompt (if any). Safe from another goroutine (rpc cancel).
func (e *Engine) Cancel() {
	if e == nil {
		return
	}
	e.runMu.Lock()
	cancel := e.runCancel
	e.runMu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// RegisterCleanup adds a function to run when the Engine is Closed — e.g. a
// background-process tool registers a stop so the process is auto-killed when
// the session ends. Cleanups run in reverse registration order (LIFO).
func (e *Engine) RegisterCleanup(fn func()) {
	if e == nil || fn == nil {
		return
	}
	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		fn() // already closed — run now so nothing leaks
		return
	}
	e.cleanups = append(e.cleanups, fn)
	e.mu.Unlock()
}

// Close cancels any in-flight run and runs registered cleanups once (LIFO).
// Idempotent. Hosts (mow run/repl, embedders) should defer Close so
// session-scoped resources — notably ext/proc background processes — are torn
// down on exit. A crashed process skips cleanups; `mow proc stop-all` recovers.
func (e *Engine) Close() error {
	if e == nil {
		return nil
	}
	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return nil
	}
	e.closed = true
	fns := e.cleanups
	e.cleanups = nil
	e.mu.Unlock()

	e.Cancel()
	for i := len(fns) - 1; i >= 0; i-- {
		func() {
			defer func() { _ = recover() }() // one bad cleanup must not skip the rest
			fns[i]()
		}()
	}
	return nil
}

// Status returns a control-plane snapshot (busy, session, model, policy flags).
func (e *Engine) Status() Status {
	if e == nil {
		return Status{}
	}
	e.runMu.Lock()
	busy := e.busy
	runID := e.runID
	e.runMu.Unlock()
	return Status{
		Busy:       busy,
		RunID:      runID,
		SessionID:  e.SessionID(),
		Workspace:  e.Workspace(),
		Model:      e.Model(),
		Wire:       e.Wire(),
		AllowWrite: e.AllowWrite(),
		AllowShell: e.AllowShell(),
	}
}

// Emit publishes an event to all OnEvent listeners. Hosts may call for pack-level
// events (e.g. acp_delegate chunks) using the current run id when busy.
func (e *Engine) Emit(ev Event) {
	if e == nil {
		return
	}
	if ev.TS.IsZero() {
		ev.TS = time.Now().UTC()
	}
	if ev.RunID == "" {
		e.runMu.Lock()
		ev.RunID = e.runID
		e.runMu.Unlock()
	}
	if ev.SessionID == "" {
		ev.SessionID = e.SessionID()
	}
	e.onTokenMu.Lock()
	subs := append([]eventSub(nil), e.onEvents...)
	e.onTokenMu.Unlock()
	for _, s := range subs {
		if s.fn != nil {
			s.fn(ev)
		}
	}
	e.log().Debug("mow event",
		"type", string(ev.Type),
		"run_id", ev.RunID,
		"session_id", ev.SessionID,
		"tool", ev.Tool,
		"stop_reason", ev.StopReason,
		"error", ev.Error,
	)
}

func (e *Engine) beginRun(parent context.Context) (ctx context.Context, runID string) {
	runID = newRunID()
	ctx, cancel := context.WithCancel(parent)
	e.runMu.Lock()
	e.busy = true
	e.runID = runID
	e.runCancel = cancel
	e.runMu.Unlock()
	return ctx, runID
}

func (e *Engine) endRun() {
	e.runMu.Lock()
	cancel := e.runCancel
	e.runCancel = nil
	e.busy = false
	e.runID = ""
	e.runMu.Unlock()
	if cancel != nil {
		// Release the run context's registration in its parent; without this a
		// long-lived host Prompting on one parent ctx accumulates children.
		cancel()
	}
}

func newRunID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("run-%d", time.Now().UnixNano())
	}
	return "run-" + hex.EncodeToString(b[:])
}
