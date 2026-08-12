package mowi

import (
	"fmt"
	"regexp"
	"strings"
	"testing"

	xansi "github.com/charmbracelet/x/ansi"
)

// extractTrueColorFGs returns 38;2;r;g;b sequences from an ANSI row.
func extractTrueColorFGs(s string) []string {
	re := regexp.MustCompile(`38;2;\d+;\d+;\d+`)
	return re.FindAllString(s, -1)
}

// extractTrueColorBGs returns 48;2;r;g;b sequences from an ANSI row.
func extractTrueColorBGs(s string) []string {
	re := regexp.MustCompile(`48;2;\d+;\d+;\d+`)
	return re.FindAllString(s, -1)
}

// sgrRGBToHex turns "48;2;r;g;b" / "38;2;r;g;b" into "#rrggbb".
func sgrRGBToHex(sgr string) string {
	// "48;2;52;96;71" → #346047
	parts := strings.Split(sgr, ";")
	if len(parts) != 5 {
		return ""
	}
	var rgb [3]int
	for i := 0; i < 3; i++ {
		n := 0
		for _, ch := range parts[2+i] {
			if ch < '0' || ch > '9' {
				return ""
			}
			n = n*10 + int(ch-'0')
		}
		if n > 255 {
			return ""
		}
		rgb[i] = n
	}
	return fmt.Sprintf("#%02x%02x%02x", rgb[0], rgb[1], rgb[2])
}

func hasBoldSGR(s string) bool {
	// lipgloss may emit "1;" at the start of a compound sequence or ";1;".
	return strings.Contains(s, "\x1b[1;") || strings.Contains(s, ";1;") || strings.Contains(s, "\x1b[1m")
}

// Context and plain identifier ink must not be pure #ffffff / near-white chrome.
// SoftDiffInk + palette chroma style are the levers; this guards the regression
// that made the structured renderer feel harsher than flashdiff.
func TestDiffContextInkIsRestrained(t *testing.T) {
	t.Setenv("MOW_FORCE_COLOR", "1")
	t.Setenv("NO_COLOR", "")
	th := newTheme()
	src := "@@ -1,1 +1,1 @@\n plain context line\n"
	out := renderPrettyDiffPath(th, src, "notes.txt", 60)
	var ctxRow string
	for _, ln := range strings.Split(out, "\n") {
		if strings.Contains(xansi.Strip(ln), "plain context") {
			ctxRow = ln
			break
		}
	}
	if ctxRow == "" {
		t.Fatalf("context row missing:\n%s", xansi.Strip(out))
	}
	// Context must carry a foreground (restrained), not band bg.
	if strings.Contains(ctxRow, "48;2;") {
		t.Fatalf("context row must not be banded: %q", ctxRow)
	}
	fgs := extractTrueColorFGs(ctxRow)
	if len(fgs) == 0 {
		t.Fatalf("context row has no foreground tint: %q", ctxRow)
	}
	// Reject pure white / pure black as the body ink (harsh on either polarity).
	for _, fg := range fgs {
		if fg == "38;2;255;255;255" || fg == "38;2;0;0;0" {
			t.Fatalf("context ink too harsh (%s): %q", fg, ctxRow)
		}
	}
}

// Go lexer path: keyword tokens differ from plain identifier ink (syntax
// layer is alive), and the add/del band still seats under them.
//
// Use a pure-add line (no equal-length replace pair) so the paint path is
// chroma-on-band rather than word chips.
func TestDiffGoSyntaxRestrainedOnBand(t *testing.T) {
	t.Setenv("MOW_FORCE_COLOR", "1")
	t.Setenv("NO_COLOR", "")
	th := newTheme()
	src := "@@ -0,0 +1,1 @@\n+func newName() int { return 2 }\n"
	out := renderPrettyDiffPath(th, src, "main.go", 80)
	var addRow string
	for _, ln := range strings.Split(out, "\n") {
		if strings.Contains(xansi.Strip(ln), "newName") {
			addRow = ln
			break
		}
	}
	if addRow == "" {
		t.Fatal("add row missing")
	}
	if !strings.Contains(addRow, "48;2;") {
		t.Fatalf("syntax path lost add band: %q", xansi.Strip(addRow))
	}
	// Multiple truecolor FGs ⇒ syntax tokens (keyword vs name), not one flat wash.
	if n := len(extractTrueColorFGs(addRow)); n < 2 {
		t.Fatalf("expected multi-token syntax FGs on Go line, got %d: %q", n, addRow)
	}
	// No pure white identifiers.
	for _, fg := range extractTrueColorFGs(addRow) {
		if fg == "38;2;255;255;255" {
			t.Fatalf("harsh white token on Go diff: %q", addRow)
		}
	}
}

// YAML path exercises a second lexer family (keys vs values).
func TestDiffYAMLSyntaxPath(t *testing.T) {
	t.Setenv("MOW_FORCE_COLOR", "1")
	t.Setenv("NO_COLOR", "")
	th := newTheme()
	src := "@@ -1,2 +1,2 @@\n-timeout: 30\n+timeout: 60\n"
	out := renderPrettyDiffPath(th, src, "config.yaml", 60)
	plain := xansi.Strip(out)
	if !strings.Contains(plain, "timeout") || !strings.Contains(plain, "60") {
		t.Fatalf("yaml body lost: %q", plain)
	}
	// Word chips should mark the changed value when paired.
	var addRow string
	for _, ln := range strings.Split(out, "\n") {
		if strings.Contains(xansi.Strip(ln), "60") {
			addRow = ln
			break
		}
	}
	if addRow == "" {
		t.Fatal("add row missing")
	}
	// Changed token carries a solid chip background (word style) or bold.
	if !strings.Contains(addRow, "48;2;") {
		t.Fatalf("yaml change row lost band/chip: %q", addRow)
	}
}

// Unknown extension: no lexer, plain restrained base styles, no panic.
func TestDiffUnknownLexerPlainSoft(t *testing.T) {
	t.Setenv("MOW_FORCE_COLOR", "1")
	t.Setenv("NO_COLOR", "")
	th := newTheme()
	src := "@@ -1,1 +1,1 @@\n-alpha beta\n+alpha gamma\n"
	out := renderPrettyDiffPath(th, src, "blob.zzz", 60)
	if !strings.Contains(xansi.Strip(out), "gamma") {
		t.Fatalf("body lost: %q", xansi.Strip(out))
	}
	// Word chips still apply without a lexer (path only gates syntax).
	var addRow string
	for _, ln := range strings.Split(out, "\n") {
		if strings.Contains(xansi.Strip(ln), "gamma") {
			addRow = ln
			break
		}
	}
	if !hasBoldSGR(addRow) && !strings.Contains(addRow, "48;2;") {
		t.Fatalf("changed token not emphasised without lexer: %q", addRow)
	}
}

// NO_COLOR: no SGR at all, signs and text still present for direction.
func TestDiffNoColorReadable(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	t.Setenv("MOW_FORCE_COLOR", "")
	th := newTheme()
	src := "@@ -1,2 +1,2 @@\n-func a() {}\n+func b() {}\n"
	out := renderPrettyDiffPath(th, src, "main.go", 60)
	if strings.Contains(out, "\x1b[") {
		t.Fatalf("NO_COLOR leaked SGR: %q", out)
	}
	plain := xansi.Strip(out)
	if !strings.Contains(plain, "\u2212") || !strings.Contains(plain, "+") {
		t.Fatalf("signs missing under NO_COLOR: %q", plain)
	}
	if !strings.Contains(plain, "func a") || !strings.Contains(plain, "func b") {
		t.Fatalf("body missing under NO_COLOR: %q", plain)
	}
}

// Word tokens: punctuation-aware segmentation chips only the edit.
func TestDiffWordPunctuationAware(t *testing.T) {
	t.Setenv("MOW_FORCE_COLOR", "1")
	t.Setenv("NO_COLOR", "")
	oldSegs, newSegs, ok := wordDiffSegs("foo(x, y)", "foo(x, z)")
	if !ok {
		t.Fatal("expected shared tokens around punctuation edit")
	}
	changedNew := ""
	for _, s := range newSegs {
		if s.changed {
			changedNew += s.text
		}
	}
	if changedNew != "z" {
		t.Fatalf("changed new segs=%q want only z; segs=%+v", changedNew, newSegs)
	}
	changedOld := ""
	for _, s := range oldSegs {
		if s.changed {
			changedOld += s.text
		}
	}
	if changedOld != "y" {
		t.Fatalf("changed old segs=%q want only y; segs=%+v", changedOld, oldSegs)
	}
}

// Whitespace-only edits land on the whitespace token, not the whole line.
func TestDiffWordWhitespaceEdit(t *testing.T) {
	oldSegs, newSegs, ok := wordDiffSegs("a  b", "a   b")
	if !ok {
		t.Fatal("expected shared a/b around whitespace edit")
	}
	var oldChanged, newChanged string
	for _, s := range oldSegs {
		if s.changed {
			oldChanged += s.text
		}
	}
	for _, s := range newSegs {
		if s.changed {
			newChanged += s.text
		}
	}
	if strings.Contains(oldChanged, "a") || strings.Contains(newChanged, "b") {
		t.Fatalf("word tokens marked changed: old=%q new=%q", oldChanged, newChanged)
	}
	if strings.TrimSpace(oldChanged) != "" || strings.TrimSpace(newChanged) != "" {
		// only spaces should be marked
		if strings.ContainsAny(oldChanged, "ab") || strings.ContainsAny(newChanged, "ab") {
			t.Fatalf("non-space in changed: old=%q new=%q", oldChanged, newChanged)
		}
	}
}

// Unicode identifiers round-trip and segment sensibly.
func TestDiffWordUnicode(t *testing.T) {
	toks := splitDiffTokens("café α1 = 2")
	if got := strings.Join(toks, ""); got != "café α1 = 2" {
		t.Fatalf("unicode round-trip: %q → %v", got, toks)
	}
	// café and α1 are word tokens, not byte-split.
	foundCafe, foundAlpha := false, false
	for _, tkn := range toks {
		if tkn == "café" {
			foundCafe = true
		}
		if tkn == "α1" {
			foundAlpha = true
		}
	}
	if !foundCafe || !foundAlpha {
		t.Fatalf("unicode words not kept intact: %v", toks)
	}
	oldSegs, newSegs, ok := wordDiffSegs("val := café", "val := θήτα")
	if !ok {
		t.Fatal("expected shared val/:= around unicode edit")
	}
	_ = oldSegs
	var ch string
	for _, s := range newSegs {
		if s.changed {
			ch += s.text
		}
	}
	if !strings.Contains(ch, "θήτα") {
		t.Fatalf("unicode change not marked: %q segs=%+v", ch, newSegs)
	}
}

// Full rewrite: no chips (noise); whole-line band only.
func TestDiffWordFullRewriteNoChips(t *testing.T) {
	t.Setenv("MOW_FORCE_COLOR", "1")
	_, _, ok := wordDiffSegs("alpha beta", "gamma delta")
	if ok {
		t.Fatal("full rewrite should not produce word segs")
	}
	th := newTheme()
	oldText, newText := emphasizeWordDiff(th, "alpha beta", "gamma delta")
	if hasBoldSGR(oldText) || hasBoldSGR(newText) {
		t.Fatalf("full rewrite should not bold chips:\n%q\n%q", oldText, newText)
	}
}

// Changed segs are bold/chip; shared segs are not bold.
func TestDiffWordEmphasizeAndDeemphasize(t *testing.T) {
	t.Setenv("MOW_FORCE_COLOR", "1")
	t.Setenv("NO_COLOR", "")
	th := newTheme()
	src := "@@ -1,1 +1,1 @@\n-timeout := 30 * time.Second\n+timeout := 60 * time.Second\n"
	out := renderPrettyDiff(th, src, 76)
	var oldRow, newRow string
	for _, ln := range strings.Split(out, "\n") {
		switch {
		case strings.Contains(xansi.Strip(ln), "30"):
			oldRow = ln
		case strings.Contains(xansi.Strip(ln), "60"):
			newRow = ln
		}
	}
	if oldRow == "" || newRow == "" {
		t.Fatalf("rows missing:\n%s", xansi.Strip(out))
	}
	// Chip (solid bg + bold) on the changed number.
	spanHas := func(row, token string, pred func(string) bool) bool {
		// Walk SGR segments; the one containing the token must match.
		parts := strings.Split(row, "\x1b[")
		for _, seg := range parts {
			if strings.Contains(seg, token) {
				return pred(seg)
			}
		}
		return false
	}
	isChip := func(seg string) bool {
		return strings.Contains(seg, "1;") || strings.HasPrefix(seg, "1m") ||
			strings.Contains(seg, ";1;") || strings.Contains(seg, "48;2;")
	}
	if !spanHas(newRow, "60", isChip) {
		t.Fatalf("changed '60' not emphasised: %q", newRow)
	}
	if !spanHas(oldRow, "30", isChip) {
		t.Fatalf("changed '30' not emphasised: %q", oldRow)
	}
	// Shared "time" should not be bold-chipped.
	if spanHas(newRow, "time", func(seg string) bool {
		return strings.HasPrefix(seg, "1;") || strings.Contains(seg, ";1;")
	}) {
		t.Fatalf("unchanged 'time' still bold: %q", newRow)
	}
}

// Split view also chips word-level edits.
func TestDiffSplitWordEmphasis(t *testing.T) {
	t.Setenv("MOW_FORCE_COLOR", "1")
	t.Setenv("NO_COLOR", "")
	th := newTheme()
	d := parseUnifiedDiff("@@ -1,1 +1,1 @@\n-timeout := 30\n+timeout := 60\n")
	out := renderDiffModel(th, d, diffPaintOpts{Width: 100, Mode: diffModeSplit, Path: "x.go", Syntax: true})
	if !strings.Contains(xansi.Strip(out), "│") {
		t.Fatalf("expected split divider: %q", xansi.Strip(out))
	}
	// At least one side should bold/chip the number.
	if !hasBoldSGR(out) && !strings.Contains(out, "48;2;") {
		t.Fatalf("split word emphasis missing: %q", out)
	}
	var saw30, saw60 bool
	for _, ln := range strings.Split(out, "\n") {
		p := xansi.Strip(ln)
		if strings.Contains(p, "30") {
			saw30 = true
		}
		if strings.Contains(p, "60") {
			saw60 = true
		}
	}
	if !saw30 || !saw60 {
		t.Fatalf("split lost body: %q", xansi.Strip(out))
	}
}

// Themes: palette-derived soft ink stays non-white across default + chroma names.
func TestDiffSoftInkAcrossThemes(t *testing.T) {
	t.Setenv("MOW_FORCE_COLOR", "1")
	t.Setenv("NO_COLOR", "")
	for _, name := range []string{"default", "catppuccin-mocha", "nord", "dracula"} {
		t.Run(name, func(t *testing.T) {
			th := newThemeFrom(ThemeConfig{Name: name}, true)
			ink := softDiffInk(th.palette)
			if ink == "" {
				t.Fatal("empty soft ink")
			}
			if ink == "#ffffff" || ink == "#FFFFFF" || ink == "#fff" {
				t.Fatalf("soft ink is pure white under theme %s: %s", name, ink)
			}
			// Highlighter builds without panic and paints something for Go.
			hl := newDiffHighlighter(th)
			if hl == nil || hl.style == nil {
				t.Fatal("highlighter missing style")
			}
			painted := hl.paintSeated("main.go", "func main() {}", th.DiffCtx)
			if painted == "" {
				t.Fatal("expected seated paint for Go")
			}
			if strings.Contains(painted, "38;2;255;255;255") {
				t.Fatalf("harsh white in paint under %s: %q", name, painted)
			}
		})
	}
}

// Painted add/del row backgrounds must clear the band floor against the theme
// surface. This is the end-to-end check: resolveDiffBg unit tests can pass
// while lipgloss emits a different wash if styles drift.
func TestRenderedDiffBandContrast(t *testing.T) {
	t.Setenv("MOW_FORCE_COLOR", "1")
	t.Setenv("NO_COLOR", "")
	// Pin the adaptive default so the surface is known (userBg), not
	// catppuccin-mocha which is DefaultThemeName when unconfigured.
	th := newThemeFrom(ThemeConfig{Name: "default"}, true)
	src := "@@ -1,2 +1,2 @@\n-timeout := 30\n+timeout := 60\n"
	out := renderPrettyDiffPath(th, src, "cfg.go", 72)
	var delRow, addRow string
	for _, ln := range strings.Split(out, "\n") {
		plain := xansi.Strip(ln)
		switch {
		case strings.Contains(plain, "30") && strings.Contains(plain, "\u2212"):
			delRow = ln
		case strings.Contains(plain, "60") && strings.Contains(plain, "+"):
			addRow = ln
		}
	}
	if delRow == "" || addRow == "" {
		t.Fatalf("missing del/add rows:\n%s", xansi.Strip(out))
	}
	delBGs := extractTrueColorBGs(delRow)
	addBGs := extractTrueColorBGs(addRow)
	if len(delBGs) == 0 || len(addBGs) == 0 {
		t.Fatalf("missing band bg SGR: del=%v add=%v", delBGs, addBGs)
	}
	// Body band is the first background on the row (gutter is unwashed).
	delHex := sgrRGBToHex(delBGs[0])
	addHex := sgrRGBToHex(addBGs[0])
	if delHex == "" || addHex == "" {
		t.Fatalf("bad SGR parse: del=%q add=%q", delBGs[0], addBGs[0])
	}
	if delHex == addHex {
		t.Fatalf("add and del share painted bg %s", addHex)
	}
	surface := th.palette.userBg
	if got := contrastRatio(delHex, surface); got < minDiffBandContrast {
		t.Errorf("del band contrast %.2f < %.2f (%s on %s)", got, minDiffBandContrast, delHex, surface)
	}
	if got := contrastRatio(addHex, surface); got < minDiffBandContrast {
		t.Errorf("add band contrast %.2f < %.2f (%s on %s)", got, minDiffBandContrast, addHex, surface)
	}
	// Restrained syntax: no pure-white tokens on the add row.
	for _, fg := range extractTrueColorFGs(addRow) {
		if fg == "38;2;255;255;255" {
			t.Fatalf("harsh white token on add row: %q", addRow)
		}
	}
	// Signs still present after the stronger wash.
	if !strings.Contains(xansi.Strip(delRow), "\u2212") || !strings.Contains(xansi.Strip(addRow), "+") {
		t.Fatalf("signs missing:\n del=%q\n add=%q", xansi.Strip(delRow), xansi.Strip(addRow))
	}
}

// Token split is lossless for representative source lines.
func TestSplitDiffTokensRoundTrip(t *testing.T) {
	cases := []string{
		"\tif x != nil {",
		"    return newClient(timeout, false)",
		"no-indent",
		"trailing space ",
		"",
		"\t\tdouble\ttab",
		"foo(x, y)",
		"café := α1",
		"key: value",
		`msg = "hi"`,
	}
	for _, in := range cases {
		if got := strings.Join(splitDiffTokens(in), ""); got != in {
			t.Fatalf("round-trip lost text: %q → %q (toks %v)", in, got, splitDiffTokens(in))
		}
	}
}

// LCS finds a mid-line shared token that prefix/suffix alone would mishandle.
func TestDiffWordLCSMidShared(t *testing.T) {
	// "a X b Y c" → "a Y b X c": both X and Y move; LCS keeps "a","b","c" shared.
	oldSegs, newSegs, ok := wordDiffSegs("a X b Y c", "a Y b X c")
	if !ok {
		t.Fatal("expected shared structure")
	}
	shared := func(segs []diffSeg) string {
		var b strings.Builder
		for _, s := range segs {
			if !s.changed {
				b.WriteString(s.text)
			}
		}
		return b.String()
	}
	if !strings.Contains(shared(oldSegs), "a") || !strings.Contains(shared(oldSegs), "b") {
		t.Fatalf("LCS lost shared tokens old=%+v", oldSegs)
	}
	if !strings.Contains(shared(newSegs), "a") || !strings.Contains(shared(newSegs), "b") {
		t.Fatalf("LCS lost shared tokens new=%+v", newSegs)
	}
}
