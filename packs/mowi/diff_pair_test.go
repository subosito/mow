package mowi

import (
	"strings"
	"testing"

	xansi "github.com/charmbracelet/x/ansi"
)

// rowsOf returns the ANSI-stripped, non-empty rows of a rendered diff.
func rowsOf(s string) []string {
	var out []string
	for _, ln := range strings.Split(s, "\n") {
		if p := xansi.Strip(ln); strings.TrimSpace(p) != "" {
			out = append(out, p)
		}
	}
	return out
}

// rowIndex is the first row containing sub, or -1.
func rowIndex(rows []string, sub string) int {
	for i, r := range rows {
		if strings.Contains(r, sub) {
			return i
		}
	}
	return -1
}

// A replaced line should read as one edit: the old row immediately followed by
// its new row, instead of every deletion then every addition.
func TestDiffPairsReplacedLines(t *testing.T) {
	th := newTheme()
	src := "@@ -10,4 +10,4 @@\n ctx before\n" +
		"-\ttimeout := 30\n-\treturn newClient(timeout, false)\n" +
		"+\ttimeout := 60\n+\treturn newClient(timeout, true)\n" +
		" ctx after\n"
	rows := rowsOf(renderPrettyDiff(th, src, 76))

	old1, new1 := rowIndex(rows, "timeout := 30"), rowIndex(rows, "timeout := 60")
	old2, new2 := rowIndex(rows, "false)"), rowIndex(rows, "true)")
	for name, i := range map[string]int{"old1": old1, "new1": new1, "old2": old2, "new2": new2} {
		if i < 0 {
			t.Fatalf("%s row missing:\n%s", name, strings.Join(rows, "\n"))
		}
	}
	if new1 != old1+1 {
		t.Fatalf("first replacement not adjacent (old=%d new=%d):\n%s", old1, new1, strings.Join(rows, "\n"))
	}
	if old2 != new1+1 || new2 != old2+1 {
		t.Fatalf("second replacement not paired (old=%d new=%d):\n%s", old2, new2, strings.Join(rows, "\n"))
	}
}

// Pairing must not corrupt the dual line-number columns.
func TestDiffPairedLineNumbers(t *testing.T) {
	th := newTheme()
	src := "@@ -10,3 +10,3 @@\n ctx\n-was\n+now\n ctx2\n"
	rows := rowsOf(renderPrettyDiff(th, src, 76))
	if len(rows) != 5 { // header + ctx + was + now + ctx2
		t.Fatalf("row count=%d:\n%s", len(rows), strings.Join(rows, "\n"))
	}
	// Old row numbers the old side (10 ctx, 11 was); new row numbers the new.
	if !strings.Contains(rows[2], "11") || !strings.Contains(rows[2], "was") {
		t.Fatalf("removed row lost its old number: %q", rows[2])
	}
	if !strings.Contains(rows[3], "11") || !strings.Contains(rows[3], "now") {
		t.Fatalf("added row lost its new number: %q", rows[3])
	}
	// Trailing context resumes at 12/12 on both sides.
	if !strings.Contains(rows[4], "12") {
		t.Fatalf("context after a pair lost numbering: %q", rows[4])
	}
}

// A deletion run with no replacement still renders every removed line.
func TestDiffPureDeletionRun(t *testing.T) {
	th := newTheme()
	src := "@@ -1,3 +1,1 @@\n-gone one\n-gone two\n keep\n"
	rows := rowsOf(renderPrettyDiff(th, src, 76))
	for _, want := range []string{"gone one", "gone two", "keep"} {
		if rowIndex(rows, want) < 0 {
			t.Fatalf("missing %q:\n%s", want, strings.Join(rows, "\n"))
		}
	}
	if got := rowIndex(rows, "gone two"); got != rowIndex(rows, "gone one")+1 {
		t.Fatalf("deletion run reordered:\n%s", strings.Join(rows, "\n"))
	}
}

// Uneven runs (1 removed, 3 added) must not drop or duplicate any line.
func TestDiffUnevenRunsKeepEveryLine(t *testing.T) {
	th := newTheme()
	src := "@@ -1,2 +1,4 @@\n-old only\n+new a\n+new b\n+new c\n"
	out := renderPrettyDiff(th, src, 76)
	rows := rowsOf(out)
	for _, want := range []string{"old only", "new a", "new b", "new c"} {
		if n := strings.Count(strings.Join(rows, "\n"), want); n != 1 {
			t.Fatalf("%q appears %d times, want exactly 1:\n%s", want, n, strings.Join(rows, "\n"))
		}
	}
}

// Within a replaced pair, only the words that actually changed are emphasised,
// so a one-token edit is findable without eyeballing both lines.
func TestDiffWordEmphasisMarksOnlyChangedWords(t *testing.T) {
	t.Setenv("MOW_FORCE_COLOR", "1")
	th := newTheme()
	src := "@@ -1,1 +1,1 @@\n-timeout := 30 * time.Second\n+timeout := 60 * time.Second\n"
	out := renderPrettyDiff(th, src, 76)

	var oldRow, newRow string
	for _, ln := range strings.Split(out, "\n") {
		switch {
		case strings.Contains(xansi.Strip(ln), "30 *"):
			oldRow = ln
		case strings.Contains(xansi.Strip(ln), "60 *"):
			newRow = ln
		}
	}
	if oldRow == "" || newRow == "" {
		t.Fatalf("rows missing:\n%s", out)
	}
	// The changed token carries bold (SGR 1); shared words do not.
	boldBefore := func(row, token string) bool {
		i := strings.Index(xansi.Strip(row), token)
		if i < 0 {
			return false
		}
		// Find the styled span that renders this token.
		for _, seg := range strings.Split(row, "\x1b[") {
			if strings.Contains(seg, token) {
				return strings.HasPrefix(seg, "1;") || strings.Contains(seg, ";1;")
			}
		}
		return false
	}
	if !boldBefore(oldRow, "30") {
		t.Fatalf("changed word not emphasised on the removed row: %q", oldRow)
	}
	if !boldBefore(newRow, "60") {
		t.Fatalf("changed word not emphasised on the added row: %q", newRow)
	}
	if boldBefore(oldRow, "time.Second") {
		t.Fatalf("unchanged word emphasised: %q", oldRow)
	}
}

// A genuine rewrite shares nothing, so per-word emphasis would be noise: the
// row falls back to a plain whole-line tint.
func TestDiffWordEmphasisFallsBackOnFullRewrite(t *testing.T) {
	th := newTheme()
	oldText, newText := emphasizeWordDiff(th, "alpha beta", "gamma delta")
	if strings.Contains(oldText, "\x1b[1m") || strings.Contains(newText, "\x1b[1m") {
		t.Fatalf("full rewrite should not emphasise words:\n%q\n%q", oldText, newText)
	}
}

// splitDiffWords must be lossless: joining the pieces reproduces the line,
// indentation included, or paired rows would silently lose whitespace.
func TestSplitDiffWordsRoundTrips(t *testing.T) {
	for _, in := range []string{
		"\tif x != nil {",
		"    return newClient(timeout, false)",
		"no-indent",
		"trailing space ",
		"",
		"\t\tdouble\ttab",
	} {
		if got := strings.Join(splitDiffWords(in), ""); got != in {
			t.Fatalf("round-trip lost text: %q -> %q", in, got)
		}
	}
}
