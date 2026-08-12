package otel

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/subosito/mow"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace/noop"
)

func TestStopStatus(t *testing.T) {
	t.Parallel()

	tests := []struct {
		reason   string
		errMsg   string
		wantCode codes.Code
		wantDesc string
	}{
		{"", "", codes.Ok, ""},
		{"completed", "", codes.Ok, ""},
		{"error", "something failed", codes.Error, "error: something failed"},
		{"max_turns", "", codes.Error, "max_turns"},
		{"cancelled", "user cancel", codes.Error, "cancelled: user cancel"},
	}

	for _, tt := range tests {
		code, desc := stopStatus(tt.reason, tt.errMsg)
		if code != tt.wantCode || desc != tt.wantDesc {
			t.Errorf("stopStatus(%q, %q) = (%v, %q), want (%v, %q)", tt.reason, tt.errMsg, code, desc, tt.wantCode, tt.wantDesc)
		}
	}
}

func TestGoalKey(t *testing.T) {
	t.Parallel()
	if got := goalKey("run1", ""); got != "" {
		t.Errorf("expected empty for empty id, got %q", got)
	}
	if got := goalKey("", "g1"); got != "g1" {
		t.Errorf("expected g1, got %q", got)
	}
	if got := goalKey("run1", "g1"); got != "run1/g1" {
		t.Errorf("expected run1/g1, got %q", got)
	}
}

func TestToolKey(t *testing.T) {
	t.Parallel()

	if got := toolKey("run1", ""); got != "" {
		t.Errorf("expected empty string for empty callID, got %q", got)
	}
	if got := toolKey("", "call1"); got != "call1" {
		t.Errorf("expected call1, got %q", got)
	}
	if got := toolKey("run1", "call1"); got != "run1/call1" {
		t.Errorf("expected run1/call1, got %q", got)
	}
}

func TestAdapterGoalEvents(t *testing.T) {
	t.Parallel()

	tr := noop.NewTracerProvider().Tracer("test")
	ad, err := New(Options{Tracer: tr})
	if err != nil {
		t.Fatal(err)
	}

	// Goal nil checks should be safe no-ops
	ad.OnEvent(mow.Event{Type: mow.EventGoalStart, Goal: nil})
	ad.OnEvent(mow.Event{Type: mow.EventGoalStep, Goal: nil})
	ad.OnEvent(mow.Event{Type: mow.EventGoalDone, Goal: nil})

	// Goal lifecycle
	g := &mow.GoalEvent{ID: "g1", Status: "running", Step: 1, MaxSteps: 5}
	ad.OnEvent(mow.Event{Type: mow.EventGoalStart, RunID: "r1", Goal: g, TS: time.Now()})
	ad.OnEvent(mow.Event{Type: mow.EventGoalStep, RunID: "r1", Goal: g, TS: time.Now()})

	// Test each end status type
	goalStatuses := []struct {
		evType mow.EventType
		status string
	}{
		{mow.EventGoalDone, "done"},
		{mow.EventGoalFail, "failed"},
		{mow.EventGoalPartial, "partial"},
		{mow.EventGoalBlocked, "blocked"},
	}

	for _, gs := range goalStatuses {
		gSub := &mow.GoalEvent{ID: "g-" + string(gs.evType), Status: gs.status, Summary: "summary"}
		ad.OnEvent(mow.Event{Type: mow.EventGoalStart, RunID: "r1", Goal: gSub, TS: time.Now()})
		ad.OnEvent(mow.Event{Type: gs.evType, RunID: "r1", Goal: gSub, TS: time.Now()})
	}
}

func TestAdapterToolDeniedAndErrors(t *testing.T) {
	t.Parallel()

	tr := noop.NewTracerProvider().Tracer("test")
	ad, err := New(Options{Tracer: tr})
	if err != nil {
		t.Fatal(err)
	}

	ts := time.Now()
	ad.OnEvent(mow.Event{Type: mow.EventRunStart, RunID: "r1", TS: ts})

	// Tool denied
	ad.OnEvent(mow.Event{Type: mow.EventToolStart, RunID: "r1", Tool: "write", ToolCallID: "c1", TS: ts})
	ad.OnEvent(mow.Event{Type: mow.EventToolEnd, RunID: "r1", Tool: "write", ToolCallID: "c1", Denied: true, DurationMs: 10, TS: ts.Add(time.Millisecond)})

	// Tool with error
	ad.OnEvent(mow.Event{Type: mow.EventToolStart, RunID: "r1", Tool: "bash", ToolCallID: "c2", TS: ts})
	ad.OnEvent(mow.Event{Type: mow.EventToolEnd, RunID: "r1", Tool: "bash", ToolCallID: "c2", Error: "command failed", DurationMs: 15, TS: ts.Add(time.Millisecond)})

	// Run end with error
	ad.OnEvent(mow.Event{Type: mow.EventRunEnd, RunID: "r1", StopReason: "error", Error: "run failed", TS: ts.Add(2 * time.Millisecond)})
}

func TestAutoWireEngineConfigPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	eng := newTestEngine(t)
	// Engine passes endpoint/service_name from config; env is merged before autoWire.
	raw := map[string]any{
		"endpoint":     srv.URL,
		"service_name": "test-env-svc",
	}
	if err := autoWire(eng, raw); err != nil {
		t.Fatalf("autoWire with engine-shaped map failed: %v", err)
	}
	if err := eng.Close(); err != nil {
		t.Fatal(err)
	}
}
