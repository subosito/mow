package lsp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/subosito/mow"
	"github.com/subosito/mow/ext"
)

func diag(sev mow.DiagnosticSeverity, msg string, line int) mow.Diagnostic {
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
		return []mow.Diagnostic{diag(mow.SeverityError, "undefined: foo", 42)}, nil
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
			out = append(out, diag(mow.SeverityWarning, fmt.Sprintf("issue %d", i), i+1))
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

// The cap must never hide an error behind a pile of hints: sort by severity
// first, so truncation drops the least severe findings.
func TestPostEditDiagnosticsErrorsSurviveTruncation(t *testing.T) {
	pull := func(context.Context, string) ([]mow.Diagnostic, error) {
		var out []mow.Diagnostic
		for i := range mow.MaxLSPDiagnostics * 2 {
			out = append(out, diag(mow.SeverityHint, fmt.Sprintf("hint %d", i), i+1))
		}
		// The one thing that matters, reported last by the server.
		out = append(out, diag(mow.SeverityError, "undefined: foo", 99))
		return out, nil
	}
	dec, err := postEditDiagnostics(pull)(context.Background(), writeEvent("a.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(dec.Result, "undefined: foo") {
		t.Fatalf("error truncated away by hints: %q", dec.Result)
	}
	// Errors render before hints.
	if strings.Index(dec.Result, "undefined: foo") > strings.Index(dec.Result, "hint 0") {
		t.Fatalf("want error first: %q", dec.Result)
	}
}

// Stable within a severity: the server's (file) order is preserved.
func TestPostEditDiagnosticsSortIsStable(t *testing.T) {
	pull := func(context.Context, string) ([]mow.Diagnostic, error) {
		return []mow.Diagnostic{
			diag(mow.SeverityWarning, "w1", 1),
			diag(mow.SeverityError, "e1", 2),
			diag(mow.SeverityWarning, "w2", 3),
			diag(mow.SeverityError, "e2", 4),
		}, nil
	}
	dec, err := postEditDiagnostics(pull)(context.Background(), writeEvent("a.go"))
	if err != nil {
		t.Fatal(err)
	}
	for _, pair := range [][2]string{{"e1", "e2"}, {"e2", "w1"}, {"w1", "w2"}} {
		if strings.Index(dec.Result, pair[0]) > strings.Index(dec.Result, pair[1]) {
			t.Fatalf("want %s before %s: %q", pair[0], pair[1], dec.Result)
		}
	}
}

// A hung language server must not hold up an edit that already succeeded.
func TestPostEditDiagnosticsTimeoutLeavesResultUnchanged(t *testing.T) {
	orig := diagTimeout
	diagTimeout = 50 * time.Millisecond
	defer func() { diagTimeout = orig }()

	blocked := make(chan struct{})
	defer close(blocked)
	pull := func(ctx context.Context, _ string) ([]mow.Diagnostic, error) {
		select {
		case <-ctx.Done(): // the hook's own deadline, not the run's
			return nil, ctx.Err()
		case <-blocked:
			return nil, nil
		}
	}
	// The run context has no deadline of its own — the hook must supply one.
	done := make(chan ext.PostToolDecision, 1)
	go func() {
		dec, err := postEditDiagnostics(pull)(context.Background(), writeEvent("a.go"))
		if err != nil {
			t.Errorf("err=%v want nil", err)
		}
		done <- dec
	}()
	select {
	case dec := <-done:
		if dec.Rewrite {
			t.Fatalf("want original result untouched, got %q", dec.Result)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("hook blocked past its own deadline")
	}
}

// Only successful edits on supported files pull diagnostics.
// Only successful edits on supported files pull diagnostics.
func TestPostEditDiagnosticsSkips(t *testing.T) {
	called := false
	pull := func(context.Context, string) ([]mow.Diagnostic, error) {
		called = true
		return []mow.Diagnostic{diag(mow.SeverityError, "x", 1)}, nil
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
		`{"kind":"full","items":[{"range":{"start":{"line":41,"character":8}},"severity":1,` +
			`"message":"undefined: foo","source":"compiler"}]}`))
	if len(got) != 1 || got[0].Line != 42 || got[0].Severity != mow.SeverityError {
		t.Fatalf("full report: %+v", got)
	}
	// Positions are 1-based in the contract, 0-based on the wire.
	if got[0].Column != 9 || got[0].Source != "compiler" {
		t.Fatalf("column/source: %+v", got[0])
	}
	// Bare item array.
	got = parseDiagnostics(json.RawMessage(
		`[{"range":{"start":{"line":0}},"severity":2,"message":"unused"}]`))
	if len(got) != 1 || got[0].Line != 1 || got[0].Severity != mow.SeverityWarning {
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
	ws := t.TempDir()
	eng, err := mow.New(mow.Options{
		NoSession: true,
		Workspace: ws,
		Chat: func(context.Context, []mow.Message, []mow.ToolSpec) (mow.Message, error) {
			return mow.Message{Role: "assistant", Content: "ok"}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = eng.Close() })
	var got []mow.Event
	eng.AddOnEvent(func(ev mow.Event) { got = append(got, ev) })
	ctx := mow.ContextWithEngine(context.Background(), eng)

	pull := func(context.Context, string) ([]mow.Diagnostic, error) {
		return []mow.Diagnostic{{
			Severity: mow.SeverityError, Message: "undefined: foo",
			Line: 42, Column: 9, Source: "compiler",
		}}, nil
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
			Column   int    `json:"column"`
			Source   string `json:"source"`
		} `json:"diagnostics"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Tool != "write" || !strings.HasSuffix(payload.Path, "internal/x/y.go") || payload.Count != 1 {
		t.Fatalf("payload=%+v", payload)
	}
	if len(payload.Diagnostics) != 1 {
		t.Fatalf("diagnostics=%+v", payload.Diagnostics)
	}
	if d := payload.Diagnostics[0]; d.Severity != "error" || d.Line != 42 ||
		d.Column != 9 || d.Source != "compiler" || d.Message != "undefined: foo" {
		t.Fatalf("diagnostic=%+v", d)
	}
}
