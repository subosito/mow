package mowi

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	xansi "github.com/charmbracelet/x/ansi"
)

// The line-number gutter is pure overhead: every cell it takes is a cell the
// code does not get. Inside the transcript's own indent, on an 80-column
// terminal, the old fixed 4-cell columns with two-space gaps put the body at
// column 17 — roughly a fifth of the width spent before any content.
//
// These tests pin the two properties that matter: the gutter is no wider than
// the numbers need, and every row still shares one geometry.

// gutterBarColumn returns the display column of the │ separator, or -1.
//
// Column, not byte offset: the elision marker uses multi-byte "·" and the sign
// column uses "−", so strings.Index would report those rows as misaligned when
// they are not. Measuring the rendered width of the prefix is the only
// correct answer for a grid.
func gutterBarColumn(line string) int {
	plain := xansi.Strip(line)
	i := strings.Index(plain, "│")
	if i < 0 {
		return -1
	}
	return lipgloss.Width(plain[:i])
}

func TestDiffGutterSizesToLineNumbers(t *testing.T) {
	t.Setenv("MOW_FORCE_COLOR", "0")
	th := newTheme()

	tests := []struct {
		name    string
		diff    string
		wantBar int // column of the separator
	}{
		// Two-digit numbers need two cells, not four.
		{"two digit", "@@ -10,3 +10,3 @@\n ctx\n-old\n+new\n", 7},
		// Four-digit numbers widen the column rather than overflowing it.
		{"four digit", "@@ -1200,3 +1200,3 @@\n ctx\n-old\n+new\n", 11},
		// A diff with no hunk header has no numbers to show, and must not
		// reserve space as if it did.
		{"no hunk header", "-old\n+new\n", 7},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := renderPrettyDiff(th, tt.diff, 100)
			for _, line := range strings.Split(out, "\n") {
				col := gutterBarColumn(line)
				if col < 0 {
					continue // hunk label row
				}
				if col != tt.wantBar {
					t.Errorf("separator at column %d, want %d:\n%s", col, tt.wantBar, line)
				}
			}
		})
	}
}

// Whatever width the gutter takes, every row must agree on it. A drifting
// separator is the failure this replaced a set of hardcoded widths to avoid.
func TestDiffGutterOneGeometryPerDiff(t *testing.T) {
	t.Setenv("MOW_FORCE_COLOR", "0")
	th := newTheme()

	diffs := map[string]string{
		"mixed widths in one diff": "@@ -8,4 +8,4 @@\n ctx\n-old\n+new\n@@ -1200,3 +1200,3 @@\n ctx2\n-a\n+b\n",
		"deletion only":            "@@ -5,2 +5,1 @@\n ctx\n-gone\n",
		"addition only":            "@@ -5,1 +5,2 @@\n ctx\n+added\n",
		"elision marker":           "@@ -10,3 +10,3 @@\n ctx\n…\n-old\n+new\n",
		"no newline marker":        "@@ -10,2 +10,2 @@\n-old\n\\ No newline at end of file\n+new\n",
	}
	for name, diff := range diffs {
		t.Run(name, func(t *testing.T) {
			out := renderPrettyDiff(th, diff, 100)
			cols := map[int][]string{}
			for _, line := range strings.Split(out, "\n") {
				if col := gutterBarColumn(line); col >= 0 {
					cols[col] = append(cols[col], line)
				}
			}
			if len(cols) > 1 {
				t.Errorf("separator drifts across columns %v:\n%s", keysOf(cols), out)
			}
		})
	}
}

func keysOf(m map[int][]string) []int {
	out := make([]int, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// A six-digit line number must not be allowed to eat the body. The column caps
// out and long numbers overflow their cell, which pushes one row right —
// better than reserving that width on every diff in the session.
func TestDiffGutterCapsWidth(t *testing.T) {
	g := newDiffGutter([]string{"@@ -1234567,3 +1234567,3 @@"})
	if g.numW != diffNumMaxWidth {
		t.Errorf("numW = %d, want the cap %d", g.numW, diffNumMaxWidth)
	}
}

// A one-line file still gets a readable gutter: a single-cell column reads as
// noise against the separator.
func TestDiffGutterHasMinimumWidth(t *testing.T) {
	g := newDiffGutter([]string{"@@ -1,1 +1,1 @@"})
	if g.numW < diffNumMinWidth {
		t.Errorf("numW = %d, want at least %d", g.numW, diffNumMinWidth)
	}
}

// The gutter must genuinely be narrower than it was, not merely rearranged.
// 17 columns was the old cost; anything near it means a regression.
func TestDiffGutterIsNarrowerThanBefore(t *testing.T) {
	t.Setenv("MOW_FORCE_COLOR", "0")
	th := newTheme()
	out := renderPrettyDiff(th, "@@ -10,3 +10,3 @@\n ctx\n-old\n+new\n", 100)

	const oldBodyColumn = 17
	for _, line := range strings.Split(out, "\n") {
		col := gutterBarColumn(line)
		if col < 0 {
			continue
		}
		body := col + 2 // separator, space, then the sign column
		if body >= oldBodyColumn {
			t.Errorf("body starts at column %d; the gutter is no narrower than before", body)
		}
	}
}

// Line numbers must still be correct after the width change: an off-by-one in
// the formatting would be invisible to alignment tests.
func TestDiffGutterNumbersStillCorrect(t *testing.T) {
	t.Setenv("MOW_FORCE_COLOR", "0")
	th := newTheme()
	out := xansi.Strip(renderPrettyDiff(th, "@@ -10,3 +10,3 @@\n ctx\n-old\n+new\n ctx2\n", 100))

	// Context rows carry both numbers; a deletion carries only the old, an
	// addition only the new.
	wants := []string{"10 10", "11   ", "   11", "12 12"}
	lines := strings.Split(out, "\n")
	var rows []string
	for _, l := range lines {
		if strings.Contains(l, "│") {
			rows = append(rows, l)
		}
	}
	if len(rows) != len(wants) {
		t.Fatalf("got %d rows, want %d:\n%s", len(rows), len(wants), out)
	}
	for i, want := range wants {
		got := rows[i][:strings.Index(rows[i], "│")]
		if strings.TrimRight(got, " ") != strings.TrimRight(" "+want, " ") {
			t.Errorf("row %d numbers = %q, want %q", i, got, " "+want)
		}
	}
}
