package mowi

import (
	"regexp"
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

// The change glyph must sit at ONE column for every row kind. Right-aligning
// it inside the number cells put "+" six columns from "−", so the eye zigzagged
// down a replace pair and the glyph failed at the only job it has: signalling
// direction where colour cannot.
func TestDiffSignsShareOneColumn(t *testing.T) {
	th := newTheme()
	src := "@@ -10,4 +10,4 @@\n ctx\n-was\n+now\n ctx2\n"
	out := xansi.Strip(renderPrettyDiff(th, src, 50))

	col := func(row, glyph string) int {
		i := strings.Index(row, glyph)
		if i < 0 {
			return -1
		}
		return len([]rune(row[:i]))
	}
	var delCol, addCol, barCols []int
	for _, ln := range strings.Split(out, "\n") {
		if i := col(ln, "│"); i >= 0 {
			barCols = append(barCols, i)
		}
		switch {
		case strings.Contains(ln, "was"):
			delCol = append(delCol, col(ln, "−"))
		case strings.Contains(ln, "now"):
			addCol = append(addCol, col(ln, "+"))
		}
	}
	if len(delCol) != 1 || len(addCol) != 1 {
		t.Fatalf("expected one del and one add row:\n%s", out)
	}
	if delCol[0] != addCol[0] {
		t.Fatalf("− at col %d but + at col %d — signs must share a column:\n%s",
			delCol[0], addCol[0], out)
	}
	// Every row's separator lines up too, so the body starts at one column.
	for _, c := range barCols {
		if c != barCols[0] {
			t.Fatalf("separator column drifts (%v):\n%s", barCols, out)
		}
	}
}

// Numbers stay numeric: the sign no longer squats in a line-number cell.
func TestDiffNumberColumnsHoldOnlyNumbers(t *testing.T) {
	th := newTheme()
	out := xansi.Strip(renderPrettyDiff(th, "@@ -1,2 +1,2 @@\n-was\n+now\n", 50))
	for _, ln := range strings.Split(out, "\n") {
		if !strings.Contains(ln, "was") && !strings.Contains(ln, "now") {
			continue
		}
		bar := strings.Index(ln, "│")
		if bar < 0 {
			t.Fatalf("row has no separator: %q", ln)
		}
		nums := ln[:bar]
		if strings.ContainsAny(nums, "+−") {
			t.Fatalf("sign leaked into the number gutter: %q", nums)
		}
	}
}

// A delete-heavy hunk must name the span that actually changed. Reporting the
// new side turned @@ -1,4 +1,1 @@ into "lines 1" — naming the one surviving
// line while hiding the three that were removed.
func TestDiffHunkLabelUsesChangedSide(t *testing.T) {
	th := newTheme()
	head := func(src string) string {
		return strings.Split(xansi.Strip(renderPrettyDiff(th, src, 50)), "\n")[0]
	}
	if got := head("@@ -1,4 +1,1 @@\n keep\n-a\n-b\n-c\n"); !strings.Contains(got, "1–4") {
		t.Fatalf("delete-heavy hunk label = %q, want the old span 1–4", got)
	}
	if got := head("@@ -1,1 +1,4 @@\n keep\n+a\n+b\n+c\n"); !strings.Contains(got, "1–4") {
		t.Fatalf("add-heavy hunk label = %q, want the new span 1–4", got)
	}
	// Pure delete keeps its dedicated wording.
	if got := head("@@ -1,3 +0,0 @@\n-a\n-b\n-c\n"); !strings.Contains(got, "removed") {
		t.Fatalf("pure delete label = %q, want 'removed'", got)
	}
}

// The wash covers the number gutter too, so a changed block is one rectangle
// rather than a body-only stripe with a notch on the left.
func TestDiffBandCoversGutter(t *testing.T) {
	t.Setenv("MOW_FORCE_COLOR", "1")
	th := newTheme()
	out := renderPrettyDiff(th, "@@ -10,2 +10,2 @@\n-was\n+now\n", 44)
	for _, ln := range strings.Split(out, "\n") {
		plain := xansi.Strip(ln)
		if !strings.Contains(plain, "was") && !strings.Contains(plain, "now") {
			continue
		}
		// The styled span covering the line number must carry a background.
		bar := strings.Index(ln, "│")
		if bar < 0 {
			t.Fatalf("no separator: %q", plain)
		}
		if !strings.Contains(ln[:bar], "48;2;") {
			t.Fatalf("number gutter is unwashed — block reads as a stripe: %q", plain)
		}
	}
}

// Changed rows paint as a full-width band, so the tint marks the change rather
// than tracing the length of each line.
func TestDiffChangedRowsFillWidth(t *testing.T) {
	t.Setenv("MOW_FORCE_COLOR", "1")
	th := newTheme()
	const width = 60
	src := "@@ -10,3 +10,3 @@\n ctx before\n-\tshort\n+\ta much longer replacement line\n ctx after\n"
	out := renderPrettyDiff(th, src, width)

	for _, ln := range strings.Split(out, "\n") {
		plain := xansi.Strip(ln)
		body := strings.TrimSpace(plain)
		if body == "" {
			continue
		}
		tinted := strings.Contains(ln, "48;2;")
		isChange := strings.Contains(plain, "short") || strings.Contains(plain, "replacement")
		switch {
		case isChange:
			if !tinted {
				t.Fatalf("changed row not tinted: %q", plain)
			}
			if w := xansi.StringWidth(plain); w != width {
				t.Fatalf("changed row width=%d, want a full %d-cell band: %q", w, width, plain)
			}
		case strings.Contains(plain, "ctx "):
			// Context must stay untinted; a band there would read as a change.
			if tinted {
				t.Fatalf("context row should not be tinted: %q", plain)
			}
			if w := xansi.StringWidth(plain); w == width {
				t.Fatalf("context row padded to full width: %q", plain)
			}
		}
	}
}

// The padding must carry the row's own tint — add and del bands stay distinct
// all the way to the right edge.
func TestDiffBandPadUsesRowColor(t *testing.T) {
	t.Setenv("MOW_FORCE_COLOR", "1")
	th := newTheme()
	out := renderPrettyDiff(th, "@@ -1,2 +1,2 @@\n-old line\n+new line\n", 44)

	bgRe := regexp.MustCompile(`48;2;\d+;\d+;\d+`)
	var delBG, addBG string
	for _, ln := range strings.Split(out, "\n") {
		plain := xansi.Strip(ln)
		// Look at the span after the last visible character: that is the pad.
		i := strings.LastIndex(ln, "e")
		if i < 0 {
			continue
		}
		found := bgRe.FindAllString(ln[i:], -1)
		switch {
		case strings.Contains(plain, "old line") && len(found) > 0:
			delBG = found[0]
		case strings.Contains(plain, "new line") && len(found) > 0:
			addBG = found[0]
		}
	}
	if delBG == "" || addBG == "" {
		t.Fatalf("pad is unstyled — band stops at the text (del=%q add=%q)\n%s", delBG, addBG, out)
	}
	if delBG == addBG {
		t.Fatalf("add and del pads share a colour %q — bands are indistinguishable", delBG)
	}
}

// Width 0 means "unknown column" (colorDiffLines): padding would invent a
// width the caller never asked for.
func TestDiffNoPadWhenWidthUnknown(t *testing.T) {
	t.Setenv("MOW_FORCE_COLOR", "1")
	th := newTheme()
	out := colorDiffLines(th, "@@ -1,2 +1,2 @@\n-x\n+y\n")
	for _, ln := range strings.Split(out, "\n") {
		plain := xansi.Strip(ln)
		if strings.HasSuffix(plain, "   ") {
			t.Fatalf("row padded despite unknown width: %q", plain)
		}
	}
}

// A line longer than the budget is still clipped, not padded past the edge.
func TestDiffBandDoesNotOverflow(t *testing.T) {
	t.Setenv("MOW_FORCE_COLOR", "1")
	th := newTheme()
	const width = 40
	long := strings.Repeat("verylongtoken ", 12)
	out := renderPrettyDiff(th, "@@ -1,2 +1,2 @@\n-"+long+"\n+"+long+"x\n", width)
	for _, ln := range strings.Split(out, "\n") {
		if w := xansi.StringWidth(xansi.Strip(ln)); w > width {
			t.Fatalf("row overflows %d cells (%d): %q", width, w, xansi.Strip(ln))
		}
	}
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
