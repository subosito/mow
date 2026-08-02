package lsp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/subosito/mow"
	"github.com/subosito/mow/ext"
)

func diag(sev, msg string, line int) mow.Diagnostic {
	return mow.Diagnostic{Severity: sev, Message: msg, Line: line}
}

func writeEvent(path string) ext.PostToolEvent {
	return ext.PostToolEvent{
		Name:       "write",
		Args:       json.RawMessage(fmt.Sprintf(`{"path":%q}`, path)),
		ToolCallID: "call-1",
		Result:     "wrote 3 lines",
	}
}

func TestPostEditDiagnosticsAppendsFindings(t *testing.T) {
	pull := func(ctx context.Context, path string) ([]mow.Diagnostic, error) {
		if path != "internal/x/y.go" {
			t.Fatalf("path=%q", path)
		}
		return []mow.Diagnostic{diag("error", "undefined: foo", 42)}, nil
	}
	dec, err := postEditDiagnostics(pull)(context.Background(), writeEvent("internal/x/y.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !dec.Rewrite {
		t.Fatal("want rewritten tool result")
	}
	if !strings.HasPrefix(dec.Result, "wrote 3 lines") {
		t.Fatalf("original result lost: %q", dec.Result)
	}
	for _, want := range []string{"lsp diagnostics", "internal/x/y.go:42", "error", "undefined: foo"} {
		if !strings.Contains(dec.Result, want) {
			t.Fatalf("result missing %q: %q", want, dec.Result)
		}
	}
}

// No LSP configured means no hook is registered at all; a nil puller is the
// same contract defensively — tool result must be untouched.
func TestPostEditDiagnosticsNoServerLeavesResultUnchanged(t *testing.T) {
	dec, err := postEditDiagnostics(nil)(context.Background(), writeEvent("a.go"))
	if err != nil {
		t.Fatal(err)
	}
	if dec.Rewrite || dec.Result != "" {
		t.Fatalf("want no-op decision, got %+v", dec)
	}
}

// A language server that errors or reports nothing must not touch a successful edit.
func TestPostEditDiagnosticsQuietWhenCleanOrFailing(t *testing.T) {
	clean := func(context.Context, string) ([]mow.Diagnostic, error) { return nil, nil }
	broken := func(context.Context, string) ([]mow.Diagnostic, error) {
		return nil, fmt.Errorf("server down")
	}
	for name, pull := range map[string]diagFunc{"clean": clean, "broken": broken} {
		dec, err := postEditDiagnostics(pull)(context.Background(), writeEvent("a.go"))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if dec.Rewrite {
			t.Fatalf("%s: want untouched result", name)
		}
	}
}

func TestPostEditDiagnosticsBounded(t *testing.T) {
	pull := func(context.Context, string) ([]mow.Diagnostic, error) {
		var out []mow.Diagnostic
		for i := range 25 {
			out = append(out, diag("warning", fmt.Sprintf("issue %d", i), i+1))
		}
		return out, nil
	}
	dec, err := postEditDiagnostics(pull)(context.Background(), writeEvent("a.go"))
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(dec.Result, "warning:"); n != mow.MaxLSPDiagnostics {
		t.Fatalf("rendered %d diagnostics, want %d", n, mow.MaxLSPDiagnostics)
	}
	if !strings.Contains(dec.Result, "25") || !strings.Contains(dec.Result, "more") {
		t.Fatalf("want total + truncation note: %q", dec.Result)
	}
}

// Only successful edits on supported files pull diagnostics.
func TestPostEditDiagnosticsSkips(t *testing.T) {
	called := false
	pull := func(context.Context, string) ([]mow.Diagnostic, error) {
		called = true
		return []mow.Diagnostic{diag("error", "x", 1)}, nil
	}
	cases := map[string]ext.PostToolEvent{
		"read tool":      {Name: "read", Args: json.RawMessage(`{"path":"a.go"}`)},
		"denied":         {Name: "write", Args: json.RawMessage(`{"path":"a.go"}`), Denied: true},
		"exec error":     {Name: "write", Args: json.RawMessage(`{"path":"a.go"}`), ExecErr: fmt.Errorf("boom")},
		"unsupported":    {Name: "write", Args: json.RawMessage(`{"path":"notes.md"}`)},
		"missing path":   {Name: "write", Args: json.RawMessage(`{}`)},
		"malformed args": {Name: "write", Args: json.RawMessage(`not json`)},
	}
	for name, ev := range cases {
		called = false
		dec, err := postEditDiagnostics(pull)(context.Background(), ev)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if called || dec.Rewrite {
			t.Fatalf("%s: diagnostics pulled for a case that must be skipped", name)
		}
	}
}

func TestParseDiagnosticsShapes(t *testing.T) {
	// Full report; LSP lines are 0-based, the event contract is 1-based.
	got := parseDiagnostics(json.RawMessage(
		`{"kind":"full","items":[{"range":{"start":{"line":41}},"severity":1,"message":"undefined: foo"}]}`))
	if len(got) != 1 || got[0].Line != 42 || got[0].Severity != "error" {
		t.Fatalf("full report: %+v", got)
	}
	// Bare item array.
	got = parseDiagnostics(json.RawMessage(
		`[{"range":{"start":{"line":0}},"severity":2,"message":"unused"}]`))
	if len(got) != 1 || got[0].Line != 1 || got[0].Severity != "warning" {
		t.Fatalf("bare array: %+v", got)
	}
	// Empty / null / garbage are all "no findings", never a panic.
	for _, raw := range []string{"", "null", "{}", "garbage"} {
		if d := parseDiagnostics(json.RawMessage(raw)); len(d) != 0 {
			t.Fatalf("%q → %+v", raw, d)
		}
	}
	// Long messages are bounded.
	long := strings.Repeat("x", maxDiagMessage*2)
	got = parseDiagnostics(json.RawMessage(fmt.Sprintf(
		`[{"range":{"start":{"line":0}},"severity":1,"message":%q}]`, long)))
	if len(got) != 1 || len([]rune(got[0].Message)) > maxDiagMessage+1 {
		t.Fatalf("message not bounded: %d", len(got[0].Message))
	}
}

// The emitted event is the frozen host contract (a TUI Problems panel parses
// exactly these fields), so assert the JSON shape, not just the struct.
func TestDiagnosticsEventPayloadShape(t *testing.T) {
	eng := &mow.Engine{}
	var got []mow.Event
	eng.AddOnEvent(func(ev mow.Event) { got = append(got, ev) })
	ctx := mow.ContextWithEngine(context.Background(), eng)

	pull := func(context.Context, string) ([]mow.Diagnostic, error) {
		return []mow.Diagnostic{diag("error", "undefined: foo", 42)}, nil
	}
	if _, err := postEditDiagnostics(pull)(ctx, writeEvent("internal/x/y.go")); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 event, got %d", len(got))
	}
	if got[0].Type != "harness.lsp.diagnostics" {
		t.Fatalf("type=%q", got[0].Type)
	}
	raw, err := json.Marshal(got[0])
	if err != nil {
		t.Fatal(err)
	}
	var payload struct {
		Type        string `json:"type"`
		Tool        string `json:"tool"`
		Path        string `json:"path"`
		Count       int    `json:"count"`
		Diagnostics []struct {
			Severity string `json:"severity"`
			Message  string `json:"message"`
			Line     int    `json:"line"`
		} `json:"diagnostics"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Tool != "write" || payload.Path != "internal/x/y.go" || payload.Count != 1 {
		t.Fatalf("payload=%+v", payload)
	}
	if len(payload.Diagnostics) != 1 || payload.Diagnostics[0].Severity != "error" ||
		payload.Diagnostics[0].Line != 42 || payload.Diagnostics[0].Message != "undefined: foo" {
		t.Fatalf("diagnostics=%+v", payload.Diagnostics)
	}
}
