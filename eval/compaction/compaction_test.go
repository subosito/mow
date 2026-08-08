package compaction_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/subosito/mow/eval/compaction"
)

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

	stub, summary, verdict, err := compaction.Compare(context.Background(), opt)
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
		t.Errorf("Net = %d, want -20000 (negative means cheaper)", got)
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
