// Package otel is an optional OpenTelemetry adapter for the mow lifecycle
// event bus. It translates [github.com/subosito/mow.Event] values into spans
// and metrics, so a host can wire any OTel exporter (OTLP, Jaeger, stdout, …)
// without mow core depending on an OTel SDK.
//
// Core mow never imports this package. Hosts opt in explicitly:
//
//	import "github.com/subosito/mow/otel"
//
//	adapter, err := otel.New(otel.Options{Tracer: tp.Tracer("mow"), Meter: mp.Meter("mow")})
//	eng.AddOnEvent(adapter.OnEvent)
//
// The adapter depends only on the go.opentelemetry.io/otel API modules
// (trace + metric + attribute + codes), not on any SDK. Pass a real
// Tracer/Meter from your exporter provider; tests may pass recording fakes.
//
// Event → telemetry mapping:
//
//	loop.run.start     → start a "mow.run" span (keyed by RunID)
//	loop.run.end       → end the run span (status from stop_reason) + token counters
//	harness.tool.start → start a "mow.tool.<name>" child span (keyed by ToolCallID)
//	harness.tool.end   → end the tool span + tool duration histogram
//	graph.goal.start   → start a "mow.goal" child span (keyed by goal id)
//	graph.goal.step    → add a "goal.step" event to the goal span
//	graph.goal.done/fail/partial/blocked → end the goal span with status
package otel

import (
	"context"
	"fmt"
	"sync"

	"github.com/subosito/mow"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

// Options configures the adapter. Either field may be nil to disable that
// signal: a nil Tracer skips all spans; a nil Meter skips all metrics.
type Options struct {
	Tracer trace.Tracer
	Meter  metric.Meter
}

// Adapter translates mow lifecycle events into OpenTelemetry spans + metrics.
// Register it as an event listener:
//
//	eng.AddOnEvent(otel.New(opts).OnEvent)
//
// OnEvent is safe for concurrent fan-out (Engine.Emit dispatches to all
// listeners). Span correlation is keyed by RunID / ToolCallID / goal id, so
// the flat event stream reconstructs nested spans without a passed context.
type Adapter struct {
	tracer trace.Tracer
	meter  metric.Meter

	inputTokens  metric.Int64Counter
	outputTokens metric.Int64Counter
	toolDuration metric.Int64Histogram

	mu    sync.Mutex
	runs  map[string]*runSpan // by RunID
	tools map[string]trace.Span
	goals map[string]trace.Span
}

// runSpan holds an in-flight run span and its context, so tool/goal spans can
// be nested under it even though the event bus carries no context.
type runSpan struct {
	span trace.Span
	ctx  context.Context
}

// New builds an adapter from Options. Metric instruments are created eagerly;
// a missing Meter leaves the metric fields nil (metric calls are no-ops).
func New(opts Options) (*Adapter, error) {
	a := &Adapter{
		tracer: opts.Tracer,
		meter:  opts.Meter,
		runs:   map[string]*runSpan{},
		tools:  map[string]trace.Span{},
		goals:  map[string]trace.Span{},
	}
	if opts.Meter != nil {
		// Errors here mean the Meter rejects the instrument name; degrade
		// gracefully to spans-only rather than failing the host.
		if c, err := opts.Meter.Int64Counter("mow.input_tokens",
			metric.WithDescription("Provider-reported input tokens per run"),
			metric.WithUnit("{token}")); err == nil {
			a.inputTokens = c
		}
		if c, err := opts.Meter.Int64Counter("mow.output_tokens",
			metric.WithDescription("Provider-reported output tokens per run"),
			metric.WithUnit("{token}")); err == nil {
			a.outputTokens = c
		}
		if h, err := opts.Meter.Int64Histogram("mow.tool_duration_ms",
			metric.WithDescription("Tool call wall duration"),
			metric.WithUnit("ms")); err == nil {
			a.toolDuration = h
		}
	}
	return a, nil
}

// OnEvent is the [mow.EventFunc] entry point. Register it via
// Engine.AddOnEvent or Options.OnEvent.
func (a *Adapter) OnEvent(ev mow.Event) {
	if a == nil {
		return
	}
	switch ev.Type {
	case mow.EventRunStart:
		a.startRun(ev)
	case mow.EventRunEnd:
		a.endRun(ev)
	case mow.EventToolStart:
		a.startTool(ev)
	case mow.EventToolEnd:
		a.endTool(ev)
	case mow.EventGoalStart:
		a.startGoal(ev)
	case mow.EventGoalStep:
		a.stepGoal(ev)
	case mow.EventGoalDone, mow.EventGoalFail, mow.EventGoalPartial, mow.EventGoalBlocked:
		a.endGoal(ev)
	}
}

// startRun opens a "mow.run" span. Even with no Tracer this still records a
// run ctx (context.Background) so nested tool/goal spans have a parent.
func (a *Adapter) startRun(ev mow.Event) {
	ctx := context.Background()
	attrs := []attribute.KeyValue{
		attribute.String("mow.run_id", ev.RunID),
	}
	if ev.SessionID != "" {
		attrs = append(attrs, attribute.String("mow.session_id", ev.SessionID))
	}
	if a.tracer != nil {
		var span trace.Span
		ctx, span = a.tracer.Start(ctx, "mow.run",
			trace.WithTimestamp(ev.TS),
			trace.WithAttributes(attrs...),
			trace.WithSpanKind(trace.SpanKindInternal),
		)
		a.mu.Lock()
		a.runs[ev.RunID] = &runSpan{span: span, ctx: ctx}
		a.mu.Unlock()
		return
	}
	a.mu.Lock()
	a.runs[ev.RunID] = &runSpan{ctx: ctx}
	a.mu.Unlock()
}

func (a *Adapter) endRun(ev mow.Event) {
	a.mu.Lock()
	rs := a.runs[ev.RunID]
	delete(a.runs, ev.RunID)
	a.mu.Unlock()
	if rs == nil {
		// No matching run.start (host emitted directly); still record tokens.
		a.recordTokens(context.Background(), ev)
		return
	}
	if rs.span != nil {
		rs.span.SetAttributes(
			attribute.String("mow.stop_reason", ev.StopReason),
			attribute.Int("mow.input_tokens", ev.InputTokens),
			attribute.Int("mow.output_tokens", ev.OutputTokens),
		)
		code, desc := stopStatus(ev.StopReason, ev.Error)
		rs.span.SetStatus(code, desc)
		if ev.Error != "" {
			rs.span.RecordError(fmt.Errorf("%s", ev.Error))
		}
		rs.span.End(trace.WithTimestamp(ev.TS))
	}
	a.recordTokens(rs.ctx, ev)
}

func (a *Adapter) startTool(ev mow.Event) {
	if a.tracer == nil {
		return
	}
	parent := a.runCtx(ev.RunID)
	if parent == nil {
		parent = context.Background()
	}
	attrs := []attribute.KeyValue{
		attribute.String("mow.run_id", ev.RunID),
		attribute.String("mow.tool", ev.Tool),
	}
	if ev.ToolCallID != "" {
		attrs = append(attrs, attribute.String("mow.tool_call_id", ev.ToolCallID))
	}
	_, span := a.tracer.Start(parent, fmt.Sprintf("mow.tool.%s", ev.Tool),
		trace.WithTimestamp(ev.TS),
		trace.WithAttributes(attrs...),
		trace.WithSpanKind(trace.SpanKindClient),
	)
	key := toolKey(ev.RunID, ev.ToolCallID)
	if key != "" {
		a.mu.Lock()
		a.tools[key] = span
		a.mu.Unlock()
	}
}

func (a *Adapter) endTool(ev mow.Event) {
	key := toolKey(ev.RunID, ev.ToolCallID)
	a.mu.Lock()
	span := a.tools[key]
	delete(a.tools, key)
	a.mu.Unlock()
	if span != nil {
		span.SetAttributes(
			attribute.Int64("mow.duration_ms", ev.DurationMs),
			attribute.Bool("mow.denied", ev.Denied),
		)
		if ev.Error != "" {
			span.SetStatus(codes.Error, ev.Error)
			span.RecordError(fmt.Errorf("%s", ev.Error))
		} else if ev.Denied {
			span.SetStatus(codes.Error, "tool denied")
		}
		span.End(trace.WithTimestamp(ev.TS))
	}
	if a.toolDuration != nil {
		a.toolDuration.Record(context.Background(), ev.DurationMs,
			metric.WithAttributes(
				attribute.String("mow.tool", ev.Tool),
			),
		)
	}
}

func (a *Adapter) startGoal(ev mow.Event) {
	g := ev.Goal
	if g == nil || a.tracer == nil {
		return
	}
	parent := a.runCtx(ev.RunID)
	if parent == nil {
		parent = context.Background()
	}
	attrs := []attribute.KeyValue{
		attribute.String("mow.run_id", ev.RunID),
		attribute.String("mow.goal_id", g.ID),
		attribute.String("mow.goal_status", g.Status),
	}
	_, span := a.tracer.Start(parent, "mow.goal",
		trace.WithTimestamp(ev.TS),
		trace.WithAttributes(attrs...),
		trace.WithSpanKind(trace.SpanKindInternal),
	)
	a.mu.Lock()
	a.goals[g.ID] = span
	a.mu.Unlock()
}

func (a *Adapter) stepGoal(ev mow.Event) {
	g := ev.Goal
	if g == nil {
		return
	}
	a.mu.Lock()
	span := a.goals[g.ID]
	a.mu.Unlock()
	if span == nil {
		return
	}
	span.AddEvent("goal.step",
		trace.WithTimestamp(ev.TS),
		trace.WithAttributes(
			attribute.Int("mow.goal_step", g.Step),
			attribute.Int("mow.goal_max_steps", g.MaxSteps),
			attribute.String("mow.goal_status", g.Status),
		),
	)
}

func (a *Adapter) endGoal(ev mow.Event) {
	g := ev.Goal
	if g == nil {
		return
	}
	a.mu.Lock()
	span := a.goals[g.ID]
	delete(a.goals, g.ID)
	a.mu.Unlock()
	if span == nil {
		return
	}
	span.SetAttributes(attribute.String("mow.goal_status", g.Status))
	switch ev.Type {
	case mow.EventGoalDone:
		span.SetStatus(codes.Ok, "")
	case mow.EventGoalFail:
		span.SetStatus(codes.Error, g.Summary)
	case mow.EventGoalPartial:
		span.SetStatus(codes.Error, "partial: "+g.Summary)
	case mow.EventGoalBlocked:
		span.SetStatus(codes.Error, "blocked")
	}
	span.End(trace.WithTimestamp(ev.TS))
}

// recordTokens emits input/output token counters on run.end.
func (a *Adapter) recordTokens(ctx context.Context, ev mow.Event) {
	if ctx == nil {
		ctx = context.Background()
	}
	attrs := []attribute.KeyValue{
		attribute.String("mow.run_id", ev.RunID),
	}
	if ev.StopReason != "" {
		attrs = append(attrs, attribute.String("mow.stop_reason", ev.StopReason))
	}
	if a.inputTokens != nil && ev.InputTokens > 0 {
		a.inputTokens.Add(ctx, int64(ev.InputTokens), metric.WithAttributes(attrs...))
	}
	if a.outputTokens != nil && ev.OutputTokens > 0 {
		a.outputTokens.Add(ctx, int64(ev.OutputTokens), metric.WithAttributes(attrs...))
	}
}

// runCtx returns the in-flight run span's context for nesting, or nil.
func (a *Adapter) runCtx(runID string) context.Context {
	a.mu.Lock()
	defer a.mu.Unlock()
	if rs := a.runs[runID]; rs != nil {
		return rs.ctx
	}
	return nil
}

// toolKey scopes an in-flight tool span so two runs' tool calls do not collide.
func toolKey(runID, callID string) string {
	if callID == "" {
		return ""
	}
	if runID == "" {
		return callID
	}
	return runID + "/" + callID
}

// stopStatus maps a mow stop_reason to an OTel span status. "completed" is OK;
// everything else (cancelled, max_turns, stuck, truncated, error) is ERROR.
func stopStatus(stopReason, errMsg string) (codes.Code, string) {
	if stopReason == "" || stopReason == "completed" {
		return codes.Ok, ""
	}
	desc := stopReason
	if errMsg != "" {
		desc = stopReason + ": " + errMsg
	}
	return codes.Error, desc
}
