package otel

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/subosito/mow"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestClampAttrRedactsAndBounds(t *testing.T) {
	t.Parallel()
	if got := clampAttr("token=supersecret"); strings.Contains(got, "supersecret") {
		t.Fatalf("secret leaked: %q", got)
	}
	if got := clampAttr("csrf_token=abc"); got != "csrf_token=abc" {
		t.Fatalf("false positive: %q", got)
	}
	long := strings.Repeat("x", maxAttrRunes+50)
	if got := clampAttr(long); len([]rune(got)) > maxAttrRunes+1 {
		t.Fatalf("not capped: %d", len([]rune(got)))
	}
}

func TestApplyEndpointUserinfo(t *testing.T) {
	t.Parallel()
	h := map[string]string{}
	applyEndpointUserinfo(h, "http://user:s3cret@collector:4318")
	if !strings.HasPrefix(h["Authorization"], "Basic ") {
		t.Fatalf("headers=%v", h)
	}
	existing := map[string]string{"Authorization": "Bearer keep"}
	applyEndpointUserinfo(existing, "http://user:s3cret@collector:4318")
	if existing["Authorization"] != "Bearer keep" {
		t.Fatal("must not overwrite existing auth")
	}
}

func TestCopyHeadersSkipsEmptyKeys(t *testing.T) {
	t.Parallel()
	got := copyHeaders(map[string]string{"": "x", "a": "b"})
	if _, ok := got[""]; ok || got["a"] != "b" {
		t.Fatalf("%v", got)
	}
}

func TestAdapterCloseEndsLeftoverSpans(t *testing.T) {
	sr := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(sr))
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })
	ad, err := New(Options{Tracer: tp.Tracer("test")})
	if err != nil {
		t.Fatal(err)
	}
	ad.OnEvent(mow.Event{Type: mow.EventRunStart, RunID: "r-left", TS: time.Now()})
	ad.OnEvent(mow.Event{Type: mow.EventToolStart, RunID: "r-left", Tool: "read", ToolCallID: "c1", TS: time.Now()})
	ad.Close()
	if n := len(sr.Ended()); n < 2 {
		t.Fatalf("want leftover spans ended, got %d", n)
	}
	ad.Close() // idempotent
}

func TestAdapterSkipsEmptyRunID(t *testing.T) {
	ad, err := New(Options{})
	if err != nil {
		t.Fatal(err)
	}
	ad.OnEvent(mow.Event{Type: mow.EventRunStart, RunID: ""})
	ad.mu.Lock()
	n := len(ad.runs)
	ad.mu.Unlock()
	if n != 0 {
		t.Fatalf("empty run id stored: %d", n)
	}
}

func TestForceFlushNilSafe(t *testing.T) {
	t.Parallel()
	var e *Export
	if err := e.ForceFlush(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestAutoWireExplicitEnabledFalse(t *testing.T) {
	eng := newTestEngine(t)
	err := autoWire(eng, map[string]any{
		"enabled":  false,
		"endpoint": "http://127.0.0.1:4318",
	})
	if err != nil {
		t.Fatal(err)
	}
}
