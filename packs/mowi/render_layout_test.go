package mowi

import (
	"strings"
	"testing"
	"time"

	"github.com/alecthomas/chroma/v2"

	xansi "github.com/charmbracelet/x/ansi"
)

// Horizontal rules must span the wrap column instead of a stubby dash.
func TestHorizontalRuleSpansWidth(t *testing.T) {
	for _, tc := range []struct {
		name  string
		width int
	}{
		{"narrow", 24},
		{"medium", 60},
		{"wide", 100},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := newMDCache(true)
			out := xansi.Strip(renderMarkdownCached(&c, "a\n\n---\n\nb\n", tc.width, false))
			if strings.Contains(out, "--------") {
				t.Fatalf("stock dash rule still present: %q", out)
			}
			var rule string
			for _, line := range strings.Split(out, "\n") {
				if strings.HasPrefix(strings.TrimSpace(line), "\u2500") {
					rule = strings.TrimSpace(line)
				}
			}
			if rule == "" {
				t.Fatalf("no box-drawing rule: %q", out)
			}
			// render() reserves 2 cells of slack; rule tracks that column.
			if w := xansi.StringWidth(rule); w < tc.width-4 || w > tc.width {
				t.Fatalf("rule width=%d want ~%d (%q)", w, tc.width, rule)
			}
		})
	}
}

// Degenerate widths still emit a minimum rule.
func TestRuleWidthFloor(t *testing.T) {
	c := newMDCache(true)
	out := xansi.Strip(renderMarkdownCached(&c, "---\n", 8, false))
	if !strings.Contains(out, "\u2500\u2500\u2500\u2500") {
		t.Fatalf("degenerate width should still emit a minimum rule: %q", out)
	}
}

// Light palettes must not paint the rule with the near-white border tone.
func TestHorizontalRuleContrastLight(t *testing.T) {
	rgb := func(hex string) string {
		r, g, b := hexRGB(hex)
		return "38;2;" + itoa(r) + ";" + itoa(g) + ";" + itoa(b)
	}
	for _, dark := range []bool{true, false} {
		p := defaultPalette(dark)
		c := newMDCache(dark)
		out := renderMarkdownCached(&c, "---\n", 60, false)
		var rule string
		for _, line := range strings.Split(out, "\n") {
			if strings.Contains(xansi.Strip(line), "\u2500") {
				rule = line
			}
		}
		if rule == "" {
			t.Fatalf("dark=%v: no rule rendered", dark)
		}
		// The rule always uses muted ink (border washes out on light surfaces).
		if !strings.Contains(rule, rgb(p.muted)) {
			t.Fatalf("dark=%v rule color missing muted %s: %q", dark, p.muted, rule)
		}
	}
}

// Fenced code sits on a left inset so it separates from surrounding prose.
func TestCodeBlockIndent(t *testing.T) {
	c := newMDCache(true)
	out := xansi.Strip(renderMarkdownCached(&c, "prose\n\n```go\nx := 1\n```\n", 60, false))
	var code string
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "x := 1") {
			code = line
		}
	}
	if code == "" {
		t.Fatalf("code line missing: %q", out)
	}
	if !strings.HasPrefix(code, "│ ") {
		t.Fatalf("code missing left rail: %q", code)
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "prose") && strings.HasPrefix(line, " ") {
			t.Fatalf("prose should stay flush: %q", line)
		}
		if strings.Contains(line, "x := 1") && strings.Contains(line, "48;2;") {
			t.Fatalf("code must be fill-free (no background): %q", line)
		}
	}
}

// Diff rows carry a +/- glyph so add/del survives a color-blind terminal.
func TestDiffRowsHaveShapeSignal(t *testing.T) {
	t.Setenv("MOW_FORCE_COLOR", "1")
	th := newTheme()
	src := "--- a.go\n+++ a.go\n@@ -10,2 +10,3 @@\n context\n-old\n+new\n"
	out := xansi.Strip(renderPrettyDiff(th, src, 80))

	var addRow, delRow, ctxRow string
	for _, line := range strings.Split(out, "\n") {
		switch {
		case strings.Contains(line, "new"):
			addRow = line
		case strings.Contains(line, "old"):
			delRow = line
		case strings.Contains(line, "context"):
			ctxRow = line
		}
	}
	if addRow == "" || delRow == "" || ctxRow == "" {
		t.Fatalf("rows missing: %q", out)
	}
	if !strings.Contains(addRow, "+") {
		t.Fatalf("added row lacks + signal: %q", addRow)
	}
	if !strings.Contains(delRow, "\u2212") {
		t.Fatalf("deleted row lacks minus signal: %q", delRow)
	}
	if strings.Contains(ctxRow, "+") || strings.Contains(ctxRow, "\u2212") {
		t.Fatalf("context row should stay unmarked: %q", ctxRow)
	}
	// Signals live in the unused number column, so the gutter stays aligned.
	wAdd, wDel, wCtx := prefixWidth(addRow), prefixWidth(delRow), prefixWidth(ctxRow)
	if wAdd != wCtx || wDel != wCtx {
		t.Fatalf("gutter misaligned: add=%d del=%d ctx=%d (%q/%q/%q)", wAdd, wDel, wCtx, addRow, delRow, ctxRow)
	}
}

// prefixWidth is the display width up to and including the gutter bar.
func prefixWidth(row string) int {
	i := strings.Index(row, "\u2502")
	if i < 0 {
		return -1
	}
	return xansi.StringWidth(row[:i])
}

// --- custom markdown renderer (markdown_render.go) coverage ---

func TestRenderInlineStyles(t *testing.T) {
	c := newMDCache(true)
	out := renderMarkdownCached(&c, "plain **bold** *italic* `code` [link](https://x.y) and ~~gone~~\n", 80, false)
	// Strong → bold SGR.
	if !strings.Contains(out, ";1m") || !strings.Contains(out, "bold") {
		t.Fatalf("strong not bold: %q", out)
	}
	// Italic.
	if !strings.Contains(out, "\x1b[3m") && !strings.Contains(out, ";3m") {
		t.Fatalf("emphasis not italic: %q", out)
	}
	// Strikethrough.
	if !strings.Contains(out, "\x1b[9m") && !strings.Contains(out, ";9m") {
		t.Fatalf("strikethrough missing: %q", out)
	}
	// Inline code is decoration-free: no underline (SGR 4 — it read as a
	// bottom border) and no background.
	if strings.Contains(out, ";4m") || strings.Contains(out, "\x1b[4m") {
		t.Fatalf("inline code must not be underlined: %q", out)
	}
	if strings.Contains(out, "48;2;") {
		t.Fatalf("inline code must be fill-free (no background): %q", out)
	}
	// Link text styled (accent rgb on the link).
	ar, ag, ab := hexRGB(defaultPalette(true).accent)
	if !strings.Contains(out, "38;2;"+itoa(ar)+";"+itoa(ag)+";"+itoa(ab)) {
		t.Fatalf("link not accent: %q", out)
	}
}

func TestRenderHeadingAccent(t *testing.T) {
	c := newMDCache(true)
	out := renderMarkdownCached(&c, "## Section\n\nbody\n", 60, false)
	ar, ag, ab := hexRGB(defaultPalette(true).accent)
	if !strings.Contains(out, "38;2;"+itoa(ar)+";"+itoa(ag)+";"+itoa(ab)) || !strings.Contains(out, "Section") {
		t.Fatalf("heading not accent-styled: %q", out)
	}
	if !strings.Contains(out, ";1m") {
		t.Fatalf("heading not bold: %q", out)
	}
}

func TestRenderListMarkers(t *testing.T) {
	c := newMDCache(true)
	out := xansi.Strip(renderMarkdownCached(&c, "- one\n- two\n\n1. first\n2. second\n", 60, false))
	if !strings.Contains(out, "• one") || !strings.Contains(out, "• two") {
		t.Fatalf("unordered markers missing: %q", out)
	}
	if !strings.Contains(out, "1. first") || !strings.Contains(out, "2. second") {
		t.Fatalf("ordered markers missing: %q", out)
	}
}

func TestRenderBlockquotePrefix(t *testing.T) {
	c := newMDCache(true)
	out := xansi.Strip(renderMarkdownCached(&c, "> quoted line\n", 60, false))
	if !strings.HasPrefix(strings.TrimSpace(out), "\u2502 ") {
		t.Fatalf("blockquote lacks gutter: %q", out)
	}
}

func TestRenderTable(t *testing.T) {
	c := newMDCache(true)
	out := xansi.Strip(renderMarkdownCached(&c, "| a | b |\n|---|---|\n| 1 | 2 |\n", 60, false))
	if !strings.Contains(out, "a │ b") || !strings.Contains(out, "1 │ 2") {
		t.Fatalf("table cells missing: %q", out)
	}
}

func TestRenderTaskList(t *testing.T) {
	c := newMDCache(true)
	out := xansi.Strip(renderMarkdownCached(&c, "- [x] done\n- [ ] todo\n", 60, false))
	if !strings.Contains(out, "[x] done") || !strings.Contains(out, "[ ] todo") {
		t.Fatalf("task markers missing: %q", out)
	}
}

func TestRenderWrapsLongLine(t *testing.T) {
	c := newMDCache(true)
	long := strings.Repeat("word ", 40) // ~200 chars
	out := xansi.Strip(renderMarkdownCached(&c, long+"\n", 40, false))
	// Rendered at wrap width 40-2=38: must span multiple lines, none wider.
	lines := strings.Split(out, "\n")
	if len(lines) < 2 {
		t.Fatalf("long line not wrapped: %q", out)
	}
	for _, ln := range lines {
		if w := xansi.StringWidth(ln); w > 38 {
			t.Fatalf("line width %d exceeds wrap: %q", w, ln)
		}
	}
}

func TestCleanLinesFastPath(t *testing.T) {
	in := "alpha padded   \nbeta  \n\n"
	got := cleanLines(in)
	want := "alpha padded\nbeta"
	if got != want {
		t.Fatalf("cleanLines(%q) = %q, want %q", in, got, want)
	}
}

func TestNormalizeFencesNoFencesPassthrough(t *testing.T) {
	md := "plain prose with no fences\nsecond line\n"
	if got := normalizeFences(md); got != md {
		t.Fatalf("normalizeFences changed plain prose: %q", got)
	}
}

func TestStabilizeMarkdownNoFencesPassthrough(t *testing.T) {
	md := "partial **bold** text with no code fence"
	if got := stabilizeMarkdown(md); got != md {
		t.Fatalf("stabilizeMarkdown changed fence-free text: %q", got)
	}
}

func TestRenderHTMLDoesNotDropContent(t *testing.T) {
	c := newMDCache(true)
	out := xansi.Strip(renderMarkdownCached(&c, "before\n\n<div class=\"x\">\nhello html\n</div>\n\nafter\n", 60, false))
	for _, want := range []string{"before", "after"} {
		if !strings.Contains(out, want) {
			t.Fatalf("content around html lost %q: %q", want, out)
		}
	}
	// HTML block text must survive (tag-free).
	if !strings.Contains(out, "hello html") {
		t.Fatalf("html block body dropped: %q", out)
	}
}

func TestRenderHeadingHierarchy(t *testing.T) {
	c := newMDCache(true)
	ac := defaultPalette(true).accent
	mu := defaultPalette(true).muted
	accRGB := func(h string) string { r, g, b := hexRGB(h); return "38;2;" + itoa(r) + ";" + itoa(g) + ";" + itoa(b) }
	tests := []struct {
		md      string
		hasAcc  bool // accent color present
		hasBold bool
	}{
		{"## Two", true, true},
		{"### Three", true, true},
		{"#### Four", false, true},
		{"##### Five", false, true},
		{"###### Six", false, false},
	}
	for _, tc := range tests {
		out := renderMarkdownCached(&c, tc.md+"\n", 60, false)
		if got := strings.Contains(out, accRGB(ac)); got != tc.hasAcc {
			t.Fatalf("%q accent=%v want %v: %q", tc.md, got, tc.hasAcc, out)
		}
		if got := strings.Contains(out, accRGB(mu)); got != !tc.hasAcc {
			t.Fatalf("%q muted=%v want %v: %q", tc.md, got, !tc.hasAcc, out)
		}
		if got := strings.Contains(out, ";1m"); got != tc.hasBold {
			t.Fatalf("%q bold=%v want %v: %q", tc.md, got, tc.hasBold, out)
		}
	}
}

func TestRenderListCompact(t *testing.T) {
	c := newMDCache(true)
	out := xansi.Strip(renderMarkdownCached(&c, "- one\n- two\n- three\n", 60, false))
	// Sibling items sit on consecutive lines (glamour's tight list rhythm).
	if !strings.Contains(out, "• one\n• two") || !strings.Contains(out, "• two\n• three") {
		t.Fatalf("list items not compact: %q", out)
	}
	// But a paragraph after the list keeps its air.
	out2 := xansi.Strip(renderMarkdownCached(&c, "- one\n- two\n\npara\n", 60, false))
	if !strings.Contains(out2, "• two\n\npara") {
		t.Fatalf("list-to-paragraph spacing lost: %q", out2)
	}
}

func TestRenderBlockquoteMuted(t *testing.T) {
	c := newMDCache(true)
	out := renderMarkdownCached(&c, "> quoted words\n", 60, false)
	r, g, b := hexRGB(defaultPalette(true).muted)
	if !strings.Contains(out, "38;2;"+itoa(r)+";"+itoa(g)+";"+itoa(b)+"mquoted") {
		t.Fatalf("quoted text not muted: %q", out)
	}
}

func TestRenderTableHeaderSeparator(t *testing.T) {
	c := newMDCache(true)
	out := xansi.Strip(renderMarkdownCached(&c, "| a | b |\n|---|---|\n| 1 | 2 |\n", 60, false))
	lines := strings.Split(out, "\n")
	// Header, separator, body — in that order.
	if len(lines) < 3 || !strings.Contains(lines[0], "a │ b") || !strings.Contains(lines[2], "1 │ 2") {
		t.Fatalf("table rows wrong order: %q", out)
	}
	if !strings.Contains(lines[1], "─") {
		t.Fatalf("header separator missing: %q", out)
	}
	// Separator columns align with the header cells (tight │ split).
	if !strings.Contains(lines[1], "─│─") {
		t.Fatalf("separator lacks column split: %q", out)
	}
}

// Every table row must share the same column layout: the │ separators line up
// even when a body cell is wider than its header (the classic misalignment).
func TestRenderTableColumnsAlignedAcrossRows(t *testing.T) {
	c := newMDCache(true)
	out := xansi.Strip(renderMarkdownCached(&c,
		"| name | role |\n|------|------|\n| peer-agent | sec |\n| looonger-name | dev |\n", 60, false))
	lines := strings.Split(out, "\n")
	if len(lines) < 4 {
		t.Fatalf("table rows missing: %q", out)
	}
	// Find each row's │ separator column (display width before the first │).
	col := func(line string) int {
		i := strings.Index(line, "│")
		if i < 0 {
			t.Fatalf("row lacks separator: %q", line)
		}
		return xansi.StringWidth(line[:i])
	}
	header, body1, body2 := col(lines[0]), col(lines[2]), col(lines[3])
	if header != body1 || body1 != body2 {
		t.Fatalf("table columns misaligned: header=%d peer-agent=%d long=%d\n%s",
			header, body1, body2, strings.Join(lines, "\n"))
	}
}

// A heading belongs to its body: one blank line above, none below.
func TestRenderHeadingHugsBody(t *testing.T) {
	c := newMDCache(true)
	out := xansi.Strip(renderMarkdownCached(&c, "intro\n\n## Section\n\nbody\n", 60, false))
	if !strings.Contains(out, "intro\n\nSection") {
		t.Fatalf("heading lost its air above: %q", out)
	}
	if !strings.Contains(out, "Section\nbody") {
		t.Fatalf("heading should hug its body (no blank below): %q", out)
	}
	if strings.Contains(out, "Section\n\nbody") {
		t.Fatalf("blank line between heading and body: %q", out)
	}
	// Back-to-back headings still separate (each keeps its air above).
	out2 := xansi.Strip(renderMarkdownCached(&c, "## One\n\n## Two\n\nx\n", 60, false))
	if !strings.Contains(out2, "One\n\nTwo") {
		t.Fatalf("consecutive headings should keep a blank between them: %q", out2)
	}
}

// Nested list markers step 4 cells per level so nesting never reads as a
// wrapped continuation line of the parent item.
func TestRenderNestedListIndent(t *testing.T) {
	c := newMDCache(true)
	out := xansi.Strip(renderMarkdownCached(&c, "- outer\n  - inner\n", 60, false))
	var inner string
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "inner") {
			inner = line
		}
	}
	if inner == "" {
		t.Fatalf("nested item missing: %q", out)
	}
	if !strings.HasPrefix(inner, "    \u2022 ") {
		t.Fatalf("nested marker should sit 4 cells in: %q", inner)
	}
	// A wrapped continuation of the parent item aligns at 2 cells — the
	// nested marker's 4-cell step must clear that column.
	long := "- " + strings.Repeat("word ", 20) + "\n  - inner\n"
	out2 := xansi.Strip(renderMarkdownCached(&c, long, 40, false))
	lines := strings.Split(out2, "\n")
	var nested string
	for _, line := range lines {
		if strings.Contains(line, "inner") {
			nested = line
		}
	}
	if nested == "" {
		t.Fatalf("wrapped list missing nested item: %q", out2)
	}
	// Parent continuations hang at the marker width, so the nested
	// marker must clear that column.
	if !strings.HasPrefix(nested, "    \u2022") {
		t.Fatalf("nested marker must clear the continuation column: %q", nested)
	}
}

// Table cells breathe: one space inside each cell, tight \u2502 separators,
// and the header rule spans the padding so columns stay aligned.
func TestRenderTableCellPadding(t *testing.T) {
	c := newMDCache(true)
	out := xansi.Strip(renderMarkdownCached(&c, "| a | b |\n|---|---|\n| 1 | 2 |\n", 60, false))
	lines := strings.Split(out, "\n")
	if len(lines) < 3 {
		t.Fatalf("table rows missing: %q", out)
	}
	if !strings.Contains(lines[0], " a \u2502 b") {
		t.Fatalf("header cells lack padding: %q", lines[0])
	}
	if !strings.Contains(lines[2], " 1 \u2502 2") {
		t.Fatalf("body cells lack padding: %q", lines[2])
	}
	if !strings.HasPrefix(lines[0], " ") || !strings.HasPrefix(lines[2], " ") {
		t.Fatalf("cells lack leading pad: %q / %q", lines[0], lines[2])
	}
	// Separator column split must land under the header's \u2502.
	hdr := xansi.StringWidth(strings.SplitN(lines[0], "\u2502", 2)[0])
	sep := xansi.StringWidth(strings.SplitN(lines[1], "\u2502", 2)[0])
	if hdr != sep {
		t.Fatalf("separator misaligned with padded header: hdr=%d sep=%d (%q / %q)", hdr, sep, lines[0], lines[1])
	}
}

func TestThemeCodeStyleRendersDiffTokens(t *testing.T) {
	th := newThemeFrom(ThemeConfig{Name: "default", Code: "monokai"}, true)
	c := newMDCacheFromTheme(th)
	out, err := c.render("```diff\n+ added\n- removed\n```", 60)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "\x1b[") {
		t.Errorf("theme.code: monokai diff block rendered uncolored: %q", out)
	}
}

// Palette-derived chroma style pins the background entry to terminal colors
// (bg:fg on bg:userBg) so chroma's default black-on-white noise never paints,
// and NO_COLOR's blank palette cannot emit chroma fallback colors.
func TestChromaStyleBackgroundPinnedToTerminal(t *testing.T) {
	// Every token the builder references must be non-empty, or chromaStyle
	// short-circuits to nil (see chromaStyle).
	st := mdStyle{fg: "#E5E7EB", muted: "#9CA3AF", accent: "#A5B4FC", meta: "#93C5FD", border: "#374151", userBg: "#2A2A2E"}
	r := &mdRenderer{st: st, width: 80}
	cs := r.chromaStyle()
	if cs == nil {
		t.Fatal("chromaStyle returned nil for a fully-colored palette")
	}
	bg := cs.Get(chroma.Background)
	if got, want := bg.Colour.String(), "#e5e7eb"; got != want {
		t.Errorf("Background fg = %q, want %q (terminal text ink)", got, want)
	}
	if bg.Background.IsSet() {
		t.Errorf("Background bg set = %q, want unset (fill-free code)", bg.Background.String())
	}
	// NO_COLOR path: blank palette yields no style at all (no token colors
	// registered), so nothing can paint.
	if got := (&mdRenderer{st: mdStyle{}, width: 80}).chromaStyle(); got != nil {
		t.Errorf("blank palette built a chroma style -- NO_COLOR must stay colorless")
	}
}

// The horizontal rule uses muted, not border: a full-width rule in border ink
// washes out on light surfaces (border sits within a few points of terminal
// bg on github-style and the built-in light palette).
func TestRuleUsesMutedNotBorder(t *testing.T) {
	st := mdStyle{fg: "#111827", muted: "#6B7280", border: "#E5E7EB"}
	r := &mdRenderer{st: st, width: 80}
	if got := r.ruleColor(); got != "#6B7280" {
		t.Errorf("ruleColor = %q, want muted #6B7280 (border #E5E7EB is a whiteout on light panes)", got)
	}
	// Blank muted (NO_COLOR) falls back to border rather than painting nothing.
	r2 := &mdRenderer{st: mdStyle{border: "#E5E7EB"}, width: 80}
	if got := r2.ruleColor(); got != "#E5E7EB" {
		t.Errorf("ruleColor blank-muted fallback = %q, want border #E5E7EB", got)
	}
}

// The faint markdown style must dim every palette token toward the terminal
// background (and drop named chroma themes) so progress surfaces cannot
// paint at full strength.
func TestMDStyleFaintDimmed(t *testing.T) {
	dark := mdStyleFromPalette(defaultPalette(true), true, "")
	f := mdStyleFaint(dark, true)
	if f.fg == dark.fg || f.accent == dark.accent || f.meta == dark.meta {
		t.Fatalf("dark faint not dimmed: fg %s->%s accent %s->%s",
			dark.fg, f.fg, dark.accent, f.accent)
	}
	if f.chromaName != "" {
		t.Fatalf("faint must drop named chroma, got %q", f.chromaName)
	}
	// Light palette dims toward white (channel values rise).
	light := mdStyleFromPalette(defaultPalette(false), false, "")
	fl := mdStyleFaint(light, false)
	if fl.fg == light.fg {
		t.Fatalf("light faint not dimmed: %s -> %s", light.fg, fl.fg)
	}
}

// The thinking indicator is progress, not content: it must paint faint.
func TestThinkingIndicatorFaint(t *testing.T) {
	m := freshModel(t)
	m.showWelcome = false
	m.reasonStartedAt = time.Now().Add(-3 * time.Second)
	line := m.renderThinkingIndicator()
	if !strings.Contains(line, "\x1b[2;") && !strings.Contains(line, ";2;") {
		t.Fatalf("thinking indicator not faint: %q", line)
	}
}

// Fenced code carries a muted heavy rail on EVERY line (the light first-line
// tick was too thin to mark the block); no background anywhere.
func TestCodeBlockRailEveryLine(t *testing.T) {
	c := newMDCache(true)
	out := xansi.Strip(renderMarkdownCached(&c, "```go\nline one\nline two\nline three\n```\n", 60, false))
	var code []string
	for _, ln := range strings.Split(out, "\n") {
		if strings.Contains(ln, "line one") || strings.Contains(ln, "line two") || strings.Contains(ln, "line three") {
			code = append(code, ln)
		}
	}
	if len(code) != 3 {
		t.Fatalf("expected 3 code lines, got %v", code)
	}
	for _, ln := range code {
		if !strings.HasPrefix(ln, "│ ") {
			t.Fatalf("code line missing rail: %q", ln)
		}
		if strings.Contains(ln, "48;2;") {
			t.Fatalf("code must be fill-free (no background): %q", ln)
		}
	}
}
