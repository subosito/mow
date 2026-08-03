package otel_test

import (
	"testing"
	"time"

	"github.com/subosito/mow"
	mowotel "github.com/subosito/mow/otel"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestAdapterRunToolSpansAndTokens(t *testing.T) {
	sr := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(sr))
	t.Cleanup(func() { _ = tp.Shutdown(t.Context()) })

	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	t.Cleanup(func() { _ = mp.Shutdown(t.Context()) })

	ad, err := mowotel.New(mowotel.Options{
		Tracer: tp.Tracer("test"),
		Meter:  mp.Meter("test"),
	})
	if err != nil {
		t.Fatal(err)
	}

	ts := time.Now()
	ad.OnEvent(mow.Event{Type: mow.EventRunStart, RunID: "r1", SessionID: "s1", TS: ts})
	ad.OnEvent(mow.Event{Type: mow.EventToolStart, RunID: "r1", Tool: "read", ToolCallID: "c1", TS: ts.Add(time.Millisecond)})
	ad.OnEvent(mow.Event{Type: mow.EventToolEnd, RunID: "r1", Tool: "read", ToolCallID: "c1", DurationMs: 12, TS: ts.Add(2 * time.Millisecond)})
	ad.OnEvent(mow.Event{
		Type: mow.EventRunEnd, RunID: "r1", StopReason: mow.StopCompleted,
		InputTokens: 10, OutputTokens: 4, TS: ts.Add(3 * time.Millisecond),
	})

	spans := sr.Ended()
	if len(spans) < 2 {
		t.Fatalf("want run+tool spans, got %d", len(spans))
	}
	names := map[string]bool{}
	for _, s := range spans {
		names[s.Name()] = true
	}
	if !names["mow.run"] || !names["mow.tool.read"] {
		t.Fatalf("missing expected spans: %v", names)
	}

	var rm metricdata.ResourceMetrics
	if err := reader.Collect(t.Context(), &rm); err != nil {
		t.Fatal(err)
	}
	gotIn, gotOut := false, false
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			switch m.Name {
			case "mow.input_tokens":
				gotIn = true
			case "mow.output_tokens":
				gotOut = true
			}
		}
	}
	if !gotIn || !gotOut {
		t.Fatalf("token metrics missing in=%v out=%v", gotIn, gotOut)
	}
}

func TestAdapterGoalSpanLifecycle(t *testing.T) {
	sr := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(sr))
	t.Cleanup(func() { _ = tp.Shutdown(t.Context()) })

	ad, err := mowotel.New(mowotel.Options{Tracer: tp.Tracer("test")})
	if err != nil {
		t.Fatal(err)
	}
	ts := time.Now()
	ad.OnEvent(mow.Event{Type: mow.EventRunStart, RunID: "r2", TS: ts})
	ad.OnEvent(mow.Event{
		Type: mow.EventGoalStart, RunID: "r2", TS: ts,
		Goal: &mow.GoalEvent{ID: "g1", Status: "running", MaxSteps: 4},
	})
	ad.OnEvent(mow.Event{
		Type: mow.EventGoalStep, RunID: "r2", TS: ts.Add(time.Millisecond),
		Goal: &mow.GoalEvent{ID: "g1", Status: "running", Step: 1, MaxSteps: 4},
	})
	ad.OnEvent(mow.Event{
		Type: mow.EventGoalDone, RunID: "r2", TS: ts.Add(2 * time.Millisecond),
		Goal: &mow.GoalEvent{ID: "g1", Status: "done", Step: 2, MaxSteps: 4, Summary: "ok"},
	})
	ad.OnEvent(mow.Event{Type: mow.EventRunEnd, RunID: "r2", StopReason: mow.StopCompleted, TS: ts.Add(3 * time.Millisecond)})

	var goal bool
	for _, s := range sr.Ended() {
		if s.Name() == "mow.goal" {
			goal = true
			if n := len(s.Events()); n < 1 {
				t.Fatalf("goal span missing step event, events=%d", n)
			}
		}
	}
	if !goal {
		t.Fatal("missing mow.goal span")
	}
}
