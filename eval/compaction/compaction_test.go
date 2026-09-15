package compaction_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/subosito/mow/eval/compaction"
)

func TestRunIncludesSummaryUsageInTotals(t *testing.T) {
	var calls, summaries atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/models") {
			_, _ = w.Write([]byte(`{"data":[{"id":"gpt-5-mini"}]}`))
			return
		}
		calls.Add(1)
		in, out := 100, 20
		content := strings.Repeat("answer ", 2000)
		if r.Header.Get("X-Mow-Component") == "turn.compact" {
			summaries.Add(1)
			in, out, content = 2000, 30, "Earlier work summarized."
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": content}, "finish_reason": "stop"}},
			"usage":   map[string]int{"prompt_tokens": in, "completion_tokens": out},
		})
	}))
	t.Cleanup(srv.Close)
	t.Setenv("MOW_HOME", t.TempDir())
	t.Setenv("MOW_API_KEY", "test-key")
	t.Setenv("MOW_MODEL", "gpt-5-mini")
	t.Setenv("MOW_BASE_URL", srv.URL)
	t.Setenv("MOW_WIRE", "openai-chat-completions")
	rep, err := compaction.Run(t.Context(), compaction.Options{
		Workspace: t.TempDir(), Summary: true, MaxContextChars: 12_000,
		Prompts: []string{"Inspect the parser.", "Explain its error handling.", "Describe a useful change.", "Review that change."},
	})
	if err != nil {
		t.Fatal(err)
	}
	summaryCalls := int(summaries.Load())
	if summaryCalls == 0 {
		t.Fatal("fixture did not exercise summarization")
	}
	regularCalls := int(calls.Load()) - summaryCalls
	wantIn, wantOut := regularCalls*100+summaryCalls*2000, regularCalls*20+summaryCalls*30
	if rep.InputTokens != wantIn || rep.OutputTokens != wantOut {
		t.Fatalf("totals=%d/%d, want %d/%d including summaries", rep.InputTokens, rep.OutputTokens, wantIn, wantOut)
	}
	if rep.SummaryInputTokens != summaryCalls*2000 || rep.SummaryOutputTokens != summaryCalls*30 {
		t.Fatalf("summary breakdown=%d/%d", rep.SummaryInputTokens, rep.SummaryOutputTokens)
	}
	// The summary overhead reverses the apparent saving from regular calls.
	baseline := compaction.Report{InputTokens: regularCalls*100 + 1000}
	if rep.Net(baseline) <= 0 {
		t.Fatal("summary overhead incorrectly reported as a token saving")
	}
}

// TestCompactionCost is the measurement item 7 depends on: does the opt-in
// compaction summarizer save more than it spends?
//
// It needs a live model endpoint and burns real tokens, so it is skipped
// unless MOW_EVAL_LIVE=1 is set. CI must never run it — the point is to
// produce a number a human reads before deciding to turn the feature on.
func TestCompactionCost(t *testing.T) {
	if os.Getenv("MOW_EVAL_LIVE") != "1" {
		t.Skip("set MOW_EVAL_LIVE=1 (and provider credentials) to measure compaction cost")
	}

	ws := t.TempDir()
	writeFixture(t, ws)

	// Prompts accumulate history on one Engine so compaction actually fires.
	// They deliberately revisit earlier work: that is where a lost thread
	// shows up as a re-read.
	prompts := []string{
		"Read every .go file in this workspace and describe what each one does.",
		"Which file defines the parser entry point? Explain how it handles errors.",
		"List every exported function across the workspace, grouped by file.",
		"Earlier you described the parser's error handling. Based on that, propose one improvement — do not re-read files you already read.",
	}

	// A small budget forces compaction within a handful of turns; real
	// budgets are ~100k+ chars and would make this eval far too expensive.
	opt := compaction.Options{
		Workspace:       ws,
		Prompts:         prompts,
		MaxContextChars: 12_000,
	}

	stub, summary, verdict, err := compaction.Compare(t.Context(), opt)
	if err != nil {
		t.Fatalf("compare: %v", err)
	}

	t.Logf("\n%s\n%s\n=> %s", stub, summary, verdict)

	if stub.Compactions == 0 && summary.Compactions == 0 {
		t.Fatal("no compaction occurred — the eval measured nothing; lower MaxContextChars")
	}
	// Deliberately not asserting that summary < stub. Model sampling makes a
	// single run noisy, and a test that fails on a coin flip trains people to
	// ignore it. The log line is the deliverable.
}

// The harness itself must be sound without a provider: a wiring bug should not
// hide behind the live-only skip.
func TestReportArithmetic(t *testing.T) {
	t.Parallel()
	stub := compaction.Report{Arm: "stub", InputTokens: 100_000}
	summary := compaction.Report{
		Arm: "summary", InputTokens: 80_000, SummaryInputTokens: 6_000,
	}
	if got := summary.Net(stub); got != -20_000 {
		t.Errorf("Net = %d, want -20000 (negative means fewer input tokens)", got)
	}
	if got := stub.Net(summary); got != 20_000 {
		t.Errorf("Net = %d, want 20000", got)
	}
	// The summary spend must be visible in the report line, or the cost side
	// of the trade is invisible to whoever reads it.
	if !strings.Contains(summary.String(), "6000") {
		t.Errorf("summary spend missing from report: %s", summary)
	}
}

func writeFixture(t *testing.T, dir string) {
	t.Helper()
	files := map[string]string{
		"parser.go": "package fix\n\n// Parse reads input.\nfunc Parse(s string) error { return nil }\n",
		"lexer.go":  "package fix\n\n// Lex splits input.\nfunc Lex(s string) []string { return nil }\n",
		"util.go":   "package fix\n\n// Clean trims input.\nfunc Clean(s string) string { return s }\n",
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}
