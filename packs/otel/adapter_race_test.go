package otel_test

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/subosito/mow"
	mowotel "github.com/subosito/mow/packs/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestAdapterConcurrentOnEvent(t *testing.T) {
	sr := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(sr))
	t.Cleanup(func() { _ = tp.Shutdown(t.Context()) })

	ad, err := mowotel.New(mowotel.Options{Tracer: tp.Tracer("test")})
	if err != nil {
		t.Fatal(err)
	}

	const runs = 32
	var wg sync.WaitGroup
	ts := time.Now()
	for i := range runs {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			runID := fmt.Sprintf("run-%d", n)
			ad.OnEvent(mow.Event{Type: mow.EventRunStart, RunID: runID, TS: ts})
			for j := range 8 {
				callID := fmt.Sprintf("c-%d", j)
				ad.OnEvent(mow.Event{
					Type: mow.EventToolStart, RunID: runID, Tool: "read",
					ToolCallID: callID, TS: ts.Add(time.Millisecond),
				})
				ad.OnEvent(mow.Event{
					Type: mow.EventToolEnd, RunID: runID, Tool: "read",
					ToolCallID: callID, DurationMs: 1, TS: ts.Add(2 * time.Millisecond),
				})
			}
			ad.OnEvent(mow.Event{
				Type: mow.EventRunEnd, RunID: runID, StopReason: mow.StopCompleted, TS: ts.Add(3 * time.Millisecond),
			})
		}(i)
	}
	wg.Wait()

	if len(sr.Ended()) < runs {
		t.Fatalf("expected at least %d run spans, got %d", runs, len(sr.Ended()))
	}
}

func TestAdapterEndRunCleansOrphanTools(t *testing.T) {
	sr := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(sr))
	t.Cleanup(func() { _ = tp.Shutdown(t.Context()) })

	ad, err := mowotel.New(mowotel.Options{Tracer: tp.Tracer("test")})
	if err != nil {
		t.Fatal(err)
	}
	ts := time.Now()
	ad.OnEvent(mow.Event{Type: mow.EventRunStart, RunID: "r-orphan", TS: ts})
	ad.OnEvent(mow.Event{
		Type: mow.EventToolStart, RunID: "r-orphan", Tool: "bash",
		ToolCallID: "orphan-call", TS: ts,
	})
	ad.OnEvent(mow.Event{
		Type: mow.EventRunEnd, RunID: "r-orphan", StopReason: mow.StopCompleted, TS: ts.Add(time.Millisecond),
	})

	var toolEnded bool
	for _, s := range sr.Ended() {
		if s.Name() == "mow.tool.bash" {
			toolEnded = true
		}
	}
	if !toolEnded {
		t.Fatal("orphan tool span was not ended on run.end")
	}
}

func TestAdapterGoalKeyScopesByRun(t *testing.T) {
	sr := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(sr))
	t.Cleanup(func() { _ = tp.Shutdown(t.Context()) })

	ad, err := mowotel.New(mowotel.Options{Tracer: tp.Tracer("test")})
	if err != nil {
		t.Fatal(err)
	}
	ts := time.Now()
	g := &mow.GoalEvent{ID: "same-id", Status: "running"}
	ad.OnEvent(mow.Event{Type: mow.EventRunStart, RunID: "r-a", TS: ts})
	ad.OnEvent(mow.Event{Type: mow.EventGoalStart, RunID: "r-a", Goal: g, TS: ts})
	ad.OnEvent(mow.Event{Type: mow.EventRunStart, RunID: "r-b", TS: ts})
	ad.OnEvent(mow.Event{Type: mow.EventGoalStart, RunID: "r-b", Goal: g, TS: ts})
	ad.OnEvent(mow.Event{Type: mow.EventGoalDone, RunID: "r-a", Goal: &mow.GoalEvent{ID: "same-id", Status: "done"}, TS: ts})
	ad.OnEvent(mow.Event{Type: mow.EventGoalDone, RunID: "r-b", Goal: &mow.GoalEvent{ID: "same-id", Status: "done"}, TS: ts})

	var goals int
	for _, s := range sr.Ended() {
		if s.Name() == "mow.goal" {
			goals++
		}
	}
	if goals != 2 {
		t.Fatalf("want 2 distinct goal spans, got %d", goals)
	}
}
