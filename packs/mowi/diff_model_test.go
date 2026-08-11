package mowi

import (
	"strings"
	"testing"

	xansi "github.com/charmbracelet/x/ansi"
)

func TestParseUnifiedDiffBasic(t *testing.T) {
	src := "--- a/pkg/main.go\n+++ b/pkg/main.go\n@@ -10,3 +10,4 @@\n ctx\n-old\n+new\n+more\n ctx2\n"
	d := parseUnifiedDiff(src)
	if d.Path != "pkg/main.go" {
		t.Fatalf("path=%q want pkg/main.go", d.Path)
	}
	if d.Adds != 2 || d.Dels != 1 {
		t.Fatalf("stats +%d −%d want +2 −1", d.Adds, d.Dels)
	}
	// Headers dropped; hunk + lines remain.
	var ops []diffLineOp
	for _, ln := range d.Lines {
		ops = append(ops, ln.Op)
	}
	want := []diffLineOp{dOpHunk, dOpCtx, dOpDel, dOpAdd, dOpAdd, dOpCtx}
	if len(ops) != len(want) {
		t.Fatalf("ops=%v want %v", ops, want)
	}
	for i := range want {
		if ops[i] != want[i] {
			t.Fatalf("ops[%d]=%v want %v (all %v)", i, ops[i], want[i], ops)
		}
	}
	// Line numbers assigned from hunk header.
	if d.Lines[1].OldNum != 10 || d.Lines[1].NewNum != 10 {
		t.Fatalf("ctx nums old=%d new=%d", d.Lines[1].OldNum, d.Lines[1].NewNum)
	}
	if d.Lines[2].OldNum != 11 || d.Lines[2].NewNum != 0 {
		t.Fatalf("del nums old=%d new=%d", d.Lines[2].OldNum, d.Lines[2].NewNum)
	}
	if d.Lines[3].OldNum != 0 || d.Lines[3].NewNum != 11 {
		t.Fatalf("add nums old=%d new=%d", d.Lines[3].OldNum, d.Lines[3].NewNum)
	}
}

func TestParseUnifiedDiffSkipsIndexAndDiffHeaders(t *testing.T) {
	src := "diff --git a/x b/x\nindex 111..222 100644\n--- a/x\n+++ b/x\n@@ -1 +1 @@\n-a\n+b\n"
	d := parseUnifiedDiff(src)
	if d.Path != "x" {
		t.Fatalf("path=%q", d.Path)
	}
	for _, ln := range d.Lines {
		if strings.HasPrefix(ln.Text, "diff ") || strings.HasPrefix(ln.Text, "index ") {
			t.Fatalf("header leaked into model: %+v", ln)
		}
	}
}

func TestParseDiffEntry(t *testing.T) {
	op, path, body := parseDiffEntry("edited pkg/main.go\n@@ -1 +1 @@\n-old\n+new\n")
	if op != "edited" || path != "pkg/main.go" {
		t.Fatalf("op=%q path=%q", op, path)
	}
	if !strings.Contains(body, "-old") {
		t.Fatalf("body=%q", body)
	}
}

func TestExpandDiffTabs(t *testing.T) {
	got := expandDiffTabs("a\tb", 4)
	if got != "a   b" {
		t.Fatalf("got %q", got)
	}
	// Already past a tab stop: one space to next stop.
	got = expandDiffTabs("abcd\tx", 4)
	if got != "abcd    x" && got != "abcdx" {
		// abcd is 4 cols → tab fills 4 spaces to next stop at 8.
		if got != "abcd    x" {
			t.Fatalf("got %q want abcd + 4 spaces + x", got)
		}
	}
}

func TestBuildSplitPairsUnequalRuns(t *testing.T) {
	d := parseUnifiedDiff("@@ -1,3 +1,2 @@\n-a\n-b\n-c\n+x\n+y\n")
	pairs := buildSplitPairs(d.Lines)
	// hunk + 3 zip rows (max(3,2)=3)
	var content int
	for _, p := range pairs {
		if p.Left != nil && p.Left.Op == dOpHunk {
			continue
		}
		content++
	}
	if content != 3 {
		t.Fatalf("content pairs=%d want 3: %+v", content, pairs)
	}
	// Third row: left del, right empty.
	last := pairs[len(pairs)-1]
	if last.Left == nil || last.Left.Op != dOpDel {
		t.Fatalf("last left=%+v", last.Left)
	}
	if last.Right != nil {
		t.Fatalf("last right should be nil for unequal run, got %+v", last.Right)
	}
}

func TestSplitModeAvailable(t *testing.T) {
	if splitModeAvailable(80) {
		t.Fatal("80 cols should not open split (min 88)")
	}
	if !splitModeAvailable(100) {
		t.Fatal("100 cols should allow split")
	}
	if splitModeAvailable(splitDiffMinWidth - 1) {
		t.Fatal("just under min should refuse")
	}
}

func TestRenderSplitFallsBackWhenNarrow(t *testing.T) {
	t.Setenv("MOW_FORCE_COLOR", "1")
	th := newTheme()
	d := parseUnifiedDiff("@@ -1,2 +1,2 @@\n-old line\n+new line\n")
	out, ok := renderDiffSplit(th, d, diffPaintOpts{Width: 40, Mode: diffModeSplit})
	if ok || out != "" {
		t.Fatalf("narrow split should refuse: ok=%v out=%q", ok, out)
	}
	// Unified via renderDiffModel still works.
	u := renderDiffModel(th, d, diffPaintOpts{Width: 40, Mode: diffModeSplit})
	if !strings.Contains(xansi.Strip(u), "old line") {
		t.Fatalf("fallback unified missing body: %q", u)
	}
}

func TestRenderSplitWideHasDivider(t *testing.T) {
	t.Setenv("MOW_FORCE_COLOR", "1")
	th := newTheme()
	d := parseUnifiedDiff("@@ -1,2 +1,2 @@\n ctx\n-old\n+new\n")
	out := xansi.Strip(renderDiffModel(th, d, diffPaintOpts{Width: 100, Mode: diffModeSplit, Path: "x.go"}))
	if !strings.Contains(out, "│") {
		t.Fatalf("split missing divider: %q", out)
	}
	if !strings.Contains(out, "old") || !strings.Contains(out, "new") {
		t.Fatalf("split missing body: %q", out)
	}
}

func TestRenderUnifiedMatchesLegacyShape(t *testing.T) {
	t.Setenv("MOW_FORCE_COLOR", "1")
	th := newTheme()
	src := "@@ -10,3 +10,3 @@\n ctx\n-was\n+now\n"
	out := xansi.Strip(renderPrettyDiff(th, src, 50))
	if !strings.Contains(out, "lines 10") {
		t.Fatalf("hunk label: %q", out)
	}
	if !strings.Contains(out, "was") || !strings.Contains(out, "now") {
		t.Fatalf("body: %q", out)
	}
	if strings.Contains(out, "--- ") || strings.Contains(out, "+++ ") {
		t.Fatalf("headers leaked: %q", out)
	}
}

func TestCollapseDiffBody(t *testing.T) {
	var b strings.Builder
	b.WriteString("@@ -1 +1 @@\n")
	for i := 0; i < 50; i++ {
		b.WriteString("+line\n")
	}
	out := collapseDiffBody(b.String(), 10)
	if !strings.Contains(out, "…") || !strings.Contains(out, "more lines") {
		t.Fatalf("fold missing: %q", out)
	}
	if strings.Count(out, "\n") > 12 {
		t.Fatalf("still too long: %d lines", strings.Count(out, "\n")+1)
	}
}

func TestLongLinesAndTabsGeometry(t *testing.T) {
	t.Setenv("MOW_FORCE_COLOR", "1")
	th := newTheme()
	long := strings.Repeat("x", 200)
	src := "@@ -1,2 +1,2 @@\n-\t" + long + "\n+\t" + long + "y\n"
	out := renderPrettyDiff(th, src, 44)
	for _, ln := range strings.Split(out, "\n") {
		if w := xansi.StringWidth(ln); w > 44 {
			t.Errorf("row wider than width: %d > 44: %q", w, xansi.Strip(ln))
		}
	}
}

func TestNoColorDiffSkipsChroma(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	t.Setenv("MOW_FORCE_COLOR", "")
	th := newTheme()
	src := "@@ -1,1 +1,1 @@\n-func main() {}\n+func main() { return }\n"
	out := renderPrettyDiffPath(th, src, "main.go", 80)
	if strings.Contains(out, "\x1b[") {
		t.Fatalf("NO_COLOR leaked SGR: %q", out)
	}
}

func TestUnknownLexerDoesNotPanic(t *testing.T) {
	t.Setenv("MOW_FORCE_COLOR", "1")
	th := newTheme()
	src := "@@ -1 +1 @@\n-a\n+b\n"
	// .zzz is not a real language; paint must fall back to plain styles.
	out := renderPrettyDiffPath(th, src, "file.zzz", 60)
	if !strings.Contains(xansi.Strip(out), "a") {
		t.Fatalf("body lost: %q", out)
	}
}

// Syntax FGs must sit ON the add/del band, not erase it. Signs stay in the
// gutter column; the body wash is the direction signal that survives a
// color-blind reading of token colours alone.
func TestSyntaxPreservesDiffBandAndSigns(t *testing.T) {
	t.Setenv("MOW_FORCE_COLOR", "1")
	t.Setenv("NO_COLOR", "")
	th := newTheme()
	src := "@@ -1,2 +1,2 @@\n-func old() {}\n+func new() {}\n"
	const width = 56
	out := renderPrettyDiffPath(th, src, "main.go", width)

	var sawDel, sawAdd bool
	for _, ln := range strings.Split(out, "\n") {
		plain := xansi.Strip(ln)
		isDel := strings.Contains(plain, "old")
		isAdd := strings.Contains(plain, "new")
		if !isDel && !isAdd {
			continue
		}
		// Band background (truecolor bg SGR) on the body.
		if !strings.Contains(ln, "48;2;") {
			t.Errorf("syntax path lost band background: %q", plain)
		}
		// Sign column still present on the change row.
		if isDel && !strings.Contains(plain, "\u2212") {
			t.Errorf("del sign missing under syntax: %q", plain)
		}
		if isAdd && !strings.Contains(plain, "+") {
			t.Errorf("add sign missing under syntax: %q", plain)
		}
		if w := xansi.StringWidth(plain); w > width {
			t.Errorf("row wider than width: %d > %d: %q", w, width, plain)
		}
		if isDel {
			sawDel = true
		}
		if isAdd {
			sawAdd = true
		}
	}
	if !sawDel || !sawAdd {
		t.Fatalf("missing del/add rows: %q", xansi.Strip(out))
	}
}
