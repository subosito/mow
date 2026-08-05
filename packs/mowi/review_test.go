package mowi

import (
	"strings"
	"testing"

	"github.com/subosito/mow/packs/review"
)

func TestIsReviewHelp(t *testing.T) {
	for _, s := range []string{"help", "-h", "--help", "?"} {
		if !isReviewHelp(s) {
			t.Fatalf("want help for %q", s)
		}
	}
	if isReviewHelp("") {
		t.Fatal("empty rest is not help")
	}
	if isReviewHelp("--staged") {
		t.Fatal("--staged is not help")
	}
}

func TestReviewSlashHelpMentionsFlags(t *testing.T) {
	h := reviewSlashHelp("review")
	for _, want := range []string{"--diff", "--staged", "--budget", "--min-severity", "cancel"} {
		if !strings.Contains(h, want) {
			t.Fatalf("help missing %q: %s", want, h)
		}
	}
}

func TestFrameReviewReport(t *testing.T) {
	rep := review.NewReport("general")
	rep.Scope.Selection = "uncommitted changes"
	rep.Scope.FilesReviewed = 3
	rep.Counts.High = 1
	rep.Counts.Low = 2
	rep.Counts.Total = 3
	rep.Findings = []review.Finding{{Title: "x"}, {}, {}}
	rep.Summary = "3 finding(s): 1 high, 2 low."
	body := "[HIGH] example" + "\n" + "  path: foo.go" + "\n"
	out := frameReviewReport("review", rep, body)
	for _, want := range []string{
		"# review · report · general",
		"scope · uncommitted changes",
		"counts · high 1 · low 2",
		"summary · 3 finding(s)",
		"---",
		"[HIGH] example",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "┌") || strings.Contains(out, "╭") {
		t.Fatalf("must not use bordered cards: %q", out)
	}
	chip := reviewStatusSummary("sec", rep)
	if !strings.Contains(chip, "sec · report · 3 findings") {
		t.Fatalf("chip=%q", chip)
	}
	if !strings.Contains(chip, "high 1") {
		t.Fatalf("chip missing severity: %q", chip)
	}
}

func TestFrameReviewReportNil(t *testing.T) {
	out := frameReviewReport("review", nil, "plain body")
	if !strings.Contains(out, "# review · report") || !strings.Contains(out, "plain body") {
		t.Fatalf("got %q", out)
	}
	if reviewStatusSummary("review", nil) != "review · report" {
		t.Fatal(reviewStatusSummary("review", nil))
	}
}
