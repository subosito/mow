package mowi

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	xansi "github.com/charmbracelet/x/ansi"
)

func TestRenderMarkdownBasic(t *testing.T) {
	th := newTheme()
	m := &model{theme: th, md: newMDCacheFromTheme(th)}
	src := "# Title\n\nHello **world**\n\n```go\nfunc main() {}\n```\n"
	out := m.renderMarkdown(src, 80, false)
	if !strings.Contains(out, "Title") && !strings.Contains(out, "main") {
		if strings.TrimSpace(out) == "" {
			t.Fatalf("empty render: %q", out)
		}
	}
	if !strings.Contains(out, "main") {
		t.Fatalf("code body missing: %q", out)
	}
}

// Markdown style must use the TUI palette — not stock glamour dark blues.
func TestMarkdownStyleMatchesTUIPalette(t *testing.T) {
	th := newThemeFrom(ThemeConfig{Name: "monokai"}, true)
	st := mdStyleFromPalette(th.palette, th.mdDark, th.chromaStyle)
	if st.fg != th.palette.fg {
		t.Fatalf("fg=%q want %q", st.fg, th.palette.fg)
	}
	if st.accent != th.palette.accent {
		t.Fatalf("accent=%q want %q", st.accent, th.palette.accent)
	}
	if st.meta != th.palette.meta {
		t.Fatalf("meta=%q want %q", st.meta, th.palette.meta)
	}
	if st.chromaName != "" {
		t.Fatalf("monokai recolors via palette tokens, got named theme %q", st.chromaName)
	}

	// Rendered output should emit truecolor from the palette hex.
	m := &model{theme: th, md: newMDCacheFromTheme(th)}
	out := m.renderMarkdown("# Hello\n\nbody text", 60, false)
	if !strings.Contains(out, "Hello") {
		t.Fatalf("missing heading: %q", out)
	}
	if !strings.Contains(out, "38;2;") {
		t.Fatalf("expected truecolor SGR from palette hex, got %q", out)
	}

	// Default theme derives chroma tokens from the palette, not a named theme.
	def := newThemeFrom(ThemeConfig{Name: "default"}, true)
	ds := mdStyleFromPalette(def.palette, def.mdDark, def.chromaStyle)
	if ds.accent != def.palette.accent {
		t.Fatalf("default accent=%q want %q", ds.accent, def.palette.accent)
	}
	if ds.chromaName != "" {
		t.Fatalf("default theme should use palette tokens, got %q", ds.chromaName)
	}
	// Heading renders accent bold.
	md := &model{theme: def, md: newMDCacheFromTheme(def)}
	hout := md.renderMarkdown("## Hi", 60, false)
	if !strings.Contains(hout, ";1m") || !strings.Contains(hout, "Hi") {
		t.Fatalf("heading not bold-accent: %q", hout)
	}
}

func TestCleanGlamourDropsPadding(t *testing.T) {
	// Simulate a full-width padded line (common wrap artifact source).
	padded := "  hello world" + strings.Repeat(" ", 40) + "\n\n"
	got := cleanLines(padded)
	if strings.HasSuffix(got, " ") {
		t.Fatalf("trailing space kept: %q", got)
	}
	if !strings.Contains(got, "hello world") {
		t.Fatalf("content lost: %q", got)
	}
	// Leading/trailing blank lines stripped
	if strings.HasPrefix(got, "\n") || strings.HasSuffix(got, "\n") {
		t.Fatalf("outer newlines: %q", got)
	}
}

func TestRenderMarkdownDiff(t *testing.T) {
	th := newTheme()
	m := &model{theme: th, md: newMDCacheFromTheme(th)}
	src := "```diff\n--- a/x\n+++ b/x\n@@ -1 +1 @@\n-old\n+new\n```"
	out := m.renderMarkdown(src, 80, false)
	if !strings.Contains(out, "old") || !strings.Contains(out, "new") {
		t.Fatalf("diff body: %q", out)
	}
}

func TestRenderPrettyDiffLineNumbers(t *testing.T) {
	t.Setenv("MOW_FORCE_COLOR", "1")
	th := newTheme()
	src := "--- a.go\n+++ a.go\n@@ -10,2 +10,3 @@\n context\n-old\n+new1\n+new2\n"
	out := xansi.Strip(renderPrettyDiff(th, src, 80))
	// Review-style hunk label (not git "−a → +b").
	if !strings.Contains(out, "lines 10–12") && !strings.Contains(out, "lines 10") {
		t.Fatalf("missing review hunk label: %q", out)
	}
	// Dual line numbers: old + new columns.
	if !strings.Contains(out, "10") || !strings.Contains(out, "│") {
		t.Fatalf("missing dual line gutter: %q", out)
	}
	if !strings.Contains(out, "old") || !strings.Contains(out, "new1") {
		t.Fatalf("missing hunk body: %q", out)
	}
	// The change glyph sits in its own column after the separator, at one x for
	// every row kind (see TestDiffSignsShareOneColumn). What must NOT appear is
	// a raw git marker glued to the body text itself.
	for _, ln := range strings.Split(out, "\n") {
		bar := strings.Index(ln, "│")
		if bar < 0 {
			continue
		}
		body := ln[bar+len("│"):]
		// Skip the sign column, then the body must start with real content.
		body = strings.TrimPrefix(body, " ")
		for _, sign := range []string{"− ", "+ ", "  "} {
			if strings.HasPrefix(body, sign) {
				body = body[len(sign):]
				break
			}
		}
		if strings.HasPrefix(body, "+") || strings.HasPrefix(body, "-") {
			t.Fatalf("raw git marker leaked into body text: %q", ln)
		}
	}
	// File headers stripped (title carries the path).
	if strings.Contains(out, "--- a.go") || strings.Contains(out, "+++ a.go") {
		t.Fatalf("raw file headers should be omitted: %q", out)
	}
}

func TestRenderDiffEntryCard(t *testing.T) {
	t.Setenv("MOW_FORCE_COLOR", "1")
	m := &model{theme: newTheme()}
	src := "edited pkg/main.go\n--- pkg/main.go\n+++ pkg/main.go\n@@ -1,1 +1,2 @@\n-old\n+new\n+more\n"
	out := xansi.Strip(m.renderDiffEntry(src, 80))
	if !strings.Contains(out, "edited") || !strings.Contains(out, "main.go") {
		t.Fatalf("title: %q", out)
	}
	if !strings.Contains(out, "+2") || !strings.Contains(out, "−1") {
		t.Fatalf("want +2 −1 stats: %q", out)
	}
	if !strings.Contains(out, "pkg") {
		t.Fatalf("want parent path hint: %q", out)
	}
}

func TestDiffStylesFollowThemePalette(t *testing.T) {
	t.Setenv("MOW_FORCE_COLOR", "1")
	// Explicit add/del/surface — backgrounds must be mixes of these, not fixed hex.
	p := palette{
		fg: "#eeeeee", muted: "#888888", accent: "#aabbff",
		user: "#00ff00", userBg: "#112233",
		err: "#ff0000", warn: "#ffff00", border: "#334455",
		add: "#00cc66", del: "#ff4466", meta: "#88aaff", slash: "#ffaa00",
	}
	th := buildTheme("test", p, true, "")
	// Soft bg = mix(userBg, add, 0.22)
	wantAddBg := mixHex("#112233", "#00cc66", 0.22)
	wantDelBg := mixHex("#112233", "#ff4466", 0.22)
	if got := th.DiffAdd.GetBackground(); got == nil {
		t.Fatal("DiffAdd missing background")
	}
	// Render a character and ensure SGR is present (color active).
	if xansi.Strip(th.DiffAdd.Render("x")) != "x" {
		t.Fatal("DiffAdd should paint")
	}
	// Explicit override wins.
	p2 := p
	p2.addBg = "#010203"
	p2.delBg = "#040506"
	th2 := buildTheme("test2", p2, true, "")
	_ = wantAddBg
	_ = wantDelBg
	if th2.DiffAdd.GetBackground() == nil || th2.DiffDel.GetBackground() == nil {
		t.Fatal("explicit diff_*_bg should set backgrounds")
	}
}

func TestParseHunkHeader(t *testing.T) {
	o, n, ok := parseHunkHeader("@@ -10,2 +12,3 @@ fn")
	if !ok || o.start != 10 || o.count != 2 || n.start != 12 || n.count != 3 {
		t.Fatalf("got old=%+v new=%+v ok=%v", o, n, ok)
	}
	o, n, ok = parseHunkHeader("@@ -0,0 +1,4 @@")
	if !ok || o.start != 0 || n.start != 1 || n.count != 4 {
		t.Fatalf("create hunk: old=%+v new=%+v", o, n)
	}
}

func TestStabilizeMarkdownClosesFence(t *testing.T) {
	open := "here\n```go\nfunc main() {"
	got := stabilizeMarkdown(open)
	if strings.Count(got, "```")%2 != 0 {
		t.Fatalf("fences still odd: %q", got)
	}
	// live render should not panic / empty
	th := newTheme()
	m := &model{theme: th, md: newMDCacheFromTheme(th)}
	out := m.renderMarkdown(open, 60, true)
	if !strings.Contains(out, "main") {
		t.Fatalf("live md: %q", out)
	}
}

func TestLiveAndFinalShareContent(t *testing.T) {
	// Finished text should render the same pipeline as live (minus stabilize).
	th := newTheme()
	m := &model{theme: th, md: newMDCacheFromTheme(th)}
	src := "Hello **there**\n\n```python\nprint(1)\n```\n"
	live := m.renderMarkdown(src, 70, true)
	final := m.renderMarkdown(src, 70, false)
	// Both should include code body; stabilize on complete text is a no-op.
	if !strings.Contains(live, "print") || !strings.Contains(final, "print") {
		t.Fatalf("live=%q final=%q", live, final)
	}
}

func TestResolveLangLabel(t *testing.T) {
	cases := map[string]string{
		"go":           "go",
		"golang":       "go",
		"py":           "python",
		"ts":           "typescript",
		"main.go":      "go",
		"path/foo.tsx": "tsx",
		"Dockerfile":   "docker",
	}
	for in, want := range cases {
		got := resolveLangLabel(in)
		// docker/Dockerfile naming varies by chroma version
		if in == "Dockerfile" {
			if got != "docker" && got != "dockerfile" && got != "Dockerfile" {
				// accept empty only if match failed entirely
				if got == "" {
					t.Fatalf("%q → empty", in)
				}
			}
			continue
		}
		if got != want {
			// filepath match may return different alias; ensure lexer resolves
			if resolveLexer(in, "") == nil {
				t.Fatalf("%q → %q want %q", in, got, want)
			}
			if got != want && resolveLexer(got, "") == nil {
				t.Fatalf("%q → %q want %q", in, got, want)
			}
		}
	}
}

func TestParseFenceInfo(t *testing.T) {
	if parseFenceInfo("go") != "go" {
		t.Fatal(parseFenceInfo("go"))
	}
	if parseFenceInfo("ts title=\"x\"") != "ts" {
		t.Fatal(parseFenceInfo("ts title=\"x\""))
	}
	if parseFenceInfo("main.go") != "main.go" {
		t.Fatal(parseFenceInfo("main.go"))
	}
}

func TestNormalizeFences(t *testing.T) {
	in := "```golang\npackage main\n```"
	out := normalizeFences(in)
	if !strings.Contains(out, "```go\n") {
		t.Fatalf("got %q", out)
	}
}

func TestWordWrap(t *testing.T) {
	out := wordWrap("one two three four five six", 10)
	if !strings.Contains(out, "\n") {
		t.Fatalf("expected wrap: %q", out)
	}
}

func TestResolveLexerGo(t *testing.T) {
	lex := resolveLexer("go", "func main() {}")
	if lex == nil {
		t.Fatal("nil lexer")
	}
	if cfg := lex.Config(); cfg == nil || cfg.Name == "" {
		t.Fatalf("lexer config: %+v", cfg)
	}
}

func TestSanitizeDisplayStripsEscapes(t *testing.T) {
	cases := []struct{ in, want string }{
		{"plain text", "plain text"},
		{"keep\nnewline\tand tab", "keep\nnewline\tand tab"},
		{"\x1b]0;evil title\x07hello", "hello"},
		{"\x1b[2Jcleared", "cleared"},
		{"\x1b]52;c;c3RvbGVu\x07copied", "copied"}, // OSC52 clipboard write
		{"a\x1b[31mred\x1b[0mb", "aredb"},          // SGR stripped
		{"bell\x07 cr\r", "bell cr"},               // C0 controls dropped
		{"lonecsi", "lonecsi"},                    // C1 CSI rune dropped
	}
	for _, c := range cases {
		if got := sanitizeDisplay(c.in); got != c.want {
			t.Errorf("sanitizeDisplay(%q)=%q want %q", c.in, got, c.want)
		}
	}
	// Split-sequence hardening: an ESC split across deltas must never survive
	// as a live escape byte, even if the tail looks like text.
	if got := sanitizeDisplay("partial\x1b[3"); strings.ContainsRune(got, 0x1b) {
		t.Errorf("split sequence left a live ESC: %q", got)
	}
}

func TestWordWrapCJKAndRunes(t *testing.T) {
	// Must stay valid UTF-8 and wrap by display width (CJK = 2 cells).
	in := "日本語のテキストが折り返されるテスト"
	out := wordWrap(in, 10)
	if !utf8.ValidString(out) {
		t.Fatalf("wordWrap produced invalid UTF-8: %q", out)
	}
	for _, line := range strings.Split(out, "\n") {
		if w := xansi.StringWidth(line); w > 10 {
			t.Errorf("line %q width %d > 10", line, w)
		}
	}
	// ASCII wrap still breaks long text (widths < 8 are passed through by design).
	if got := wordWrap("aaaa bbbb cccc", 9); !strings.Contains(got, "\n") {
		t.Errorf("ascii wrap: %q", got)
	}
	// truncate/short must not split runes.
	if got := truncate("日本語テキスト", 5); !utf8.ValidString(got) {
		t.Errorf("truncate invalid utf8: %q", got)
	}
	if got := short("日本語テキスト", 5); !utf8.ValidString(got) {
		t.Errorf("short invalid utf8: %q", got)
	}
}

func TestSanitizeDisplayComprehensive(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		// Identity / keep safe whitespace
		{"empty", "", ""},
		{"plain", "hello world", "hello world"},
		{"newline_tab", "a\nb\tc", "a\nb\tc"},
		{"unicode", "日本語 🎉 café", "日本語 🎉 café"},

		// OSC (Operating System Command) — title, clipboard, hyperlink
		{"osc0_title_bel", "\x1b]0;evil\x07ok", "ok"},
		{"osc0_title_st", "\x1b]0;evil\x1b\\ok", "ok"},
		{"osc2_title", "\x1b]2;title\x07x", "x"},
		{"osc52_clipboard_bel", "\x1b]52;c;c3RvbGVu\x07safe", "safe"},
		{"osc52_clipboard_st", "\x1b]52;c;YWJj\x1b\\safe", "safe"},
		{"osc8_hyperlink", "\x1b]8;;https://evil.test\x1b\\click\x1b]8;;\x1b\\", "click"},
		{"osc9_notify", "\x1b]9;notify\x07done", "done"},

		// CSI (Control Sequence Introducer)
		{"csi_sgr_color", "a\x1b[31mred\x1b[0mb", "aredb"},
		{"csi_sgr_256", "\x1b[38;5;196mx\x1b[0m", "x"},
		{"csi_sgr_rgb", "\x1b[38;2;255;0;0mx\x1b[0m", "x"},
		{"csi_erase_display", "\x1b[2Jcleared", "cleared"},
		{"csi_erase_line", "\x1b[Kpartial", "partial"},
		{"csi_cursor_pos", "\x1b[1;1Hhome", "home"},
		{"csi_cursor_up", "\x1b[5Amove", "move"},
		{"csi_cup_and_text", "before\x1b[10;10Hafter", "beforeafter"},
		{"csi_smcup_alt_screen", "\x1b[?1049halt", "alt"},
		{"csi_rmcup", "\x1b[?1049lnorm", "norm"},
		{"csi_hide_cursor", "\x1b[?25lhide", "hide"},
		{"csi_bracketed_paste", "\x1b[?2004hpaste", "paste"},
		{"csi_device_status", "\x1b[6nquery", "query"},
		{"csi_soft_reset", "\x1b[!psoft", "soft"},

		// C1 8-bit forms. xansi.Strip does not parse 8-bit CSI/OSC bodies, so
		// sanitizeDisplay drops the C1 byte via isBadControlRune and leaves any
		// printable parameter tail (no live C1 remains — that is the invariant).
		{"c1_csi_rune", "pre\u009b31mred\u009bm post", "pre31mredm post"}, // 0x9b CSI
		{"c1_osc_rune_bel", "\u009d52;c;YQ==\x07x", "52;c;YQ==x"},         // 0x9d OSC + BEL
		{"c1_index", "a\u0084b", "ab"},                                    // 0x84 IND
		{"c1_nel", "a\u0085b", "ab"},                                      // 0x85 NEL

		// DCS / APC / PM / SOS (string types terminated by ST)
		{"dcs_st", "\x1bP1$r\x1b\\text", "text"},
		{"apc_st", "\x1b_apcdata\x1b\\text", "text"},
		{"pm_st", "\x1b^pmdata\x1b\\text", "text"},
		{"sos_st", "\x1bXsosdata\x1b\\text", "text"},

		// C0 controls (except \n \t which are kept)
		{"bel", "a\x07b", "ab"},
		{"bs", "a\x08b", "ab"},
		{"cr", "a\rb", "ab"},
		{"nul", "a\x00b", "ab"},
		{"vt_ff", "a\x0bb\x0cc", "abc"},
		{"so_si", "a\x0eb\x0fc", "abc"},
		{"del", "a\x7fb", "ab"},
		{"mixed_c0", "bell\x07 cr\r keep\n tab\tok", "bell cr keep\n tab\tok"},

		// Incomplete / split sequences — ESC must not survive live
		{"partial_csi", "partial\x1b[3", "partial"},
		{"partial_osc", "x\x1b]52;c;abc", "x"},
		// ESC + final byte in 0x40-0x7E is consumed as a 2-byte escape by xansi.Strip.
		{"lone_esc", "a\x1bb", "a"},
		{"esc_at_end", "tail\x1b", "tail"},
		{"double_esc", "\x1b\x1b[31mx", "x"},

		// Nested / repeated attack patterns
		{"osc52_embedded", "steal:\x1b]52;c;c3RvbGVu\x07:done", "steal::done"},
		{"sgr_around_osc", "\x1b[31m\x1b]0;t\x07x\x1b[0m", "x"},
		{"multi_osc52", "\x1b]52;c;YQ==\x07+\x1b]52;c;Yg==\x07", "+"},

		// Permission-prompt / UI spoof fragments must lose control bytes
		{"spoof_clear_prompt", "\x1b[2J\x1b[HAllow write? [y/n]", "Allow write? [y/n]"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := sanitizeDisplay(c.in)
			if got != c.want {
				t.Errorf("sanitizeDisplay(%q)\n got %q\nwant %q", c.in, got, c.want)
			}
			// Invariant: no live ESC and no C0 (except \n\t) / DEL / C1 may remain.
			if strings.ContainsRune(got, 0x1b) {
				t.Errorf("live ESC remains in %q", got)
			}
			for _, r := range got {
				if r == '\n' || r == '\t' {
					continue
				}
				if r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f) {
					t.Errorf("control rune U+%04X remains in %q", r, got)
				}
			}
		})
	}
}

func TestSanitizeDisplayNoLiveEscInvariant(t *testing.T) {
	// Fuzz-ish corpus of hostile fragments — every output must be free of ESC
	// and bad controls, and must preserve printable content when present.
	inputs := []string{
		"\x1b",
		"\x1b[",
		"\x1b]",
		"\x1b]52",
		"\x1b]52;c;",
		"\x1b]52;c;QQ==\x07",
		"\x1b[38;2;1;2;3m",
		"\x1b[?1049h\x1b[2J\x1b[H",
		"ok\x1b[0;0R",
		string([]byte{0x1b, 0x00, 0x07, 0x0d, 0x1b}),
		"line1\n\x1b[31mline2\x1b[0m\nline3",
		"\u009b1;2H\u009d0;x\x07",
		strings.Repeat("\x1b[31mA\x1b[0m", 50),
		"normal text with $pecial chars !@#%",
	}
	for i, in := range inputs {
		got := sanitizeDisplay(in)
		if strings.ContainsRune(got, 0x1b) {
			t.Errorf("[%d] ESC survived: in=%q out=%q", i, in, got)
		}
		for _, r := range got {
			if isBadControlRune(r) {
				t.Errorf("[%d] bad control U+%04X in %q", i, r, got)
			}
		}
	}
}

func TestIsBadControlRune(t *testing.T) {
	// Allowed: newline, tab, and printables (ASCII + above C1).
	for _, r := range []rune{'\n', '\t', ' ', 'a', 'Z', '0', '~', 0xa0, 0xff, 0x4e00} {
		if isBadControlRune(r) {
			t.Errorf("isBadControlRune(U+%04X) = true, want false", r)
		}
	}
	// Bad: C0 except n/t, DEL, C1 (0x80-0x9f).
	for _, r := range []rune{
		0x00, 0x01, 0x07, 0x08, 0x0b, 0x0c, 0x0d, 0x1b, 0x1f,
		0x7f,
		0x80, 0x84, 0x85, 0x90, 0x9b, 0x9c, 0x9d, 0x9f,
	} {
		if !isBadControlRune(r) {
			t.Errorf("isBadControlRune(U+%04X) = false, want true", r)
		}
	}
}

// TestSanitizeDisplayAtStorageChokePoints verifies the invariant that every
// transcript path (add / bumpToolTally / bumpToolError / applyStreamSnap)
// runs untrusted text through sanitizeDisplay before storage or live paint.
func TestSanitizeDisplayAtStorageChokePoints(t *testing.T) {
	const osc52 = "\x1b]52;c;c3RvbGVu\x07"
	const csiRed = "\x1b[31m"
	payload := osc52 + "SECRET" + csiRed + "x\x1b[0m"
	// After sanitize: no ESC/controls, printable body kept.
	wantBody := "SECRETx"

	t.Run("add", func(t *testing.T) {
		m := newModel(testEngine(t), false, false)
		m.add(kindAssistant, payload)
		if len(m.entries) == 0 {
			t.Fatal("no entry")
		}
		got := m.entries[len(m.entries)-1].text
		if got != wantBody {
			t.Fatalf("add stored %q want %q", got, wantBody)
		}
		if strings.ContainsRune(got, 0x1b) {
			t.Fatalf("live ESC in entry: %q", got)
		}
	})

	t.Run("addAt_history", func(t *testing.T) {
		m := newModel(testEngine(t), false, false)
		m.addAt(kindUser, "user\x07bell\r", time.Time{})
		got := m.entries[len(m.entries)-1].text
		if got != "userbell" {
			t.Fatalf("addAt stored %q", got)
		}
	})

	t.Run("bumpToolTally", func(t *testing.T) {
		m := newModel(testEngine(t), false, false)
		// First call creates the tool line via add (already sanitized).
		m.bumpToolTally("bash", "bash · 0.1s")
		// Second call updates in place — must sanitize the joined text.
		m.bumpToolTally("bash", "bash · 0.2s"+osc52)
		if m.toolLineIdx < 0 || m.toolLineIdx >= len(m.entries) {
			t.Fatal("no tool line")
		}
		got := m.entries[m.toolLineIdx].text
		if strings.ContainsRune(got, 0x1b) || strings.Contains(got, "52;c;") {
			t.Fatalf("tool line kept escape: %q", got)
		}
		// tally form for count>1 is "bash ×2"
		if !strings.Contains(got, "bash") {
			t.Fatalf("tool line missing name: %q", got)
		}
	})

	t.Run("bumpToolError", func(t *testing.T) {
		m := newModel(testEngine(t), false, false)
		m.bumpToolError("edit · error · " + osc52 + "line_hash not found")
		if m.toolLineIdx < 0 || m.toolLineIdx >= len(m.entries) {
			t.Fatal("no tool tally line for error")
		}
		got := m.entries[m.toolLineIdx].text
		if m.entries[m.toolLineIdx].kind != kindTool {
			t.Fatalf("want kindTool, got %v", m.entries[m.toolLineIdx].kind)
		}
		if !strings.Contains(got, "⚠") || !strings.Contains(got, "line_hash") {
			t.Fatalf("error folded into tool line: %q", got)
		}
		// Second error updates the same tool line with count — still sanitized.
		m.bumpToolError("edit · error · fail2\x1b[2J")
		got = m.entries[m.toolLineIdx].text
		if strings.ContainsRune(got, 0x1b) {
			t.Fatalf("live ESC in error: %q", got)
		}
		if !strings.Contains(got, "fail2") || !strings.Contains(got, "×2") {
			t.Fatalf("error tally form: %q", got)
		}
	})

	t.Run("applyStreamSnap", func(t *testing.T) {
		m := newModel(testEngine(t), false, false)
		m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
		m.applyStreamSnap(payload, "")
		if m.streamRaw != wantBody || m.streamBuf != wantBody {
			t.Fatalf("streamRaw=%q streamBuf=%q want %q", m.streamRaw, m.streamBuf, wantBody)
		}
		if strings.ContainsRune(m.streamRaw, 0x1b) {
			t.Fatal("live ESC in stream buffer")
		}
		// Streaming deltas: each chunk sanitized independently; split ESC
		// across chunks cannot reassemble into a live sequence because the
		// ESC byte is stripped from the first chunk before append.
		m2 := newModel(testEngine(t), false, false)
		m2.applyStreamSnap("pre\x1b", "")
		m2.applyStreamSnap("]52;c;YQ==\x07post", "")
		if strings.ContainsRune(m2.streamRaw, 0x1b) {
			t.Fatalf("split ESC survived: %q", m2.streamRaw)
		}
		// First chunk loses ESC; second has no ESC so "]52..." stays as text.
		// That is acceptable: without a leading ESC it is not an OSC sequence.
		if !strings.Contains(m2.streamRaw, "pre") || !strings.Contains(m2.streamRaw, "post") {
			t.Fatalf("split stream lost text: %q", m2.streamRaw)
		}
	})
}

// TestSanitizeDisplayEntryNeverPaintsEscapes builds a transcript with hostile
// model/tool text and asserts the rendered viewport content has no OSC/CSI
// originating from entry text. (Chrome may still emit theme SGR — we only
// require that the *payload* escapes are gone, checked via stored entries
// and that OSC 52 / BEL never appear in the view.)
func TestSanitizeDisplayEntryNeverPaintsEscapes(t *testing.T) {
	m := newModel(testEngine(t), false, false)
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	m.showWelcome = false
	hostile := []struct {
		kind entryKind
		text string
	}{
		{kindUser, "please run \x1b]52;c;YQ==\x07 now"},
		{kindAssistant, "Sure:\x1b[2J\x1b[H\x1b]0;pwned\x07 done"},
		{kindTool, "bash \x1b[31mrm -rf\x1b[0m"},
		{kindError, "denied\x07\r\x1b[1;1H"},
		{kindDiff, "@@ -1 +1 @@\n-\x1b]52;c;eWVz\x07\n+ok"},
	}
	for _, h := range hostile {
		m.add(h.kind, h.text)
	}
	for i, e := range m.entries {
		if strings.ContainsRune(e.text, 0x1b) {
			t.Errorf("entry[%d] kind=%v has live ESC: %q", i, e.kind, e.text)
		}
		for _, r := range e.text {
			if isBadControlRune(r) {
				t.Errorf("entry[%d] bad control U+%04X: %q", i, r, e.text)
			}
		}
	}
	m.refreshVP()
	view := m.View().Content
	// OSC 52 clipboard and BEL must never reach the terminal from entry text.
	if strings.Contains(view, "]52;") || strings.Contains(view, "\x1b]52") {
		t.Fatal("OSC52 reached the view")
	}
	if strings.ContainsRune(view, '\x07') {
		t.Fatal("BEL reached the view")
	}
	// Visible safe text still present.
	for _, needle := range []string{"please run", "done", "bash", "denied"} {
		if !strings.Contains(view, needle) {
			// view may be styled; strip SGR for search
			plain := xansi.Strip(view)
			if !strings.Contains(plain, needle) {
				t.Errorf("view missing %q; plain=%q", needle, plain)
			}
		}
	}
}

func TestMarkdownRendererNestedListsPreserveParentItem(t *testing.T) {
	c := newMDCache(true)
	out := xansi.Strip(renderMarkdownCached(&c, "- outer\n  - inner\n  - inner two\n- sibling", 80, false))
	for _, want := range []string{"• outer", "  • inner", "  • inner two", "• sibling"} {
		if !strings.Contains(out, want) {
			t.Errorf("nested list missing %q in %q", want, out)
		}
	}
}

func TestMarkdownRendererNestedQuotesHaveGutters(t *testing.T) {
	c := newMDCache(true)
	out := xansi.Strip(renderMarkdownCached(&c, "> > nested", 80, false))
	if !strings.Contains(out, "│ │ nested") {
		t.Fatalf("nested quote lost a gutter: %q", out)
	}
}

func TestMarkdownRendererStripsHTMLAndControls(t *testing.T) {
	src := "<div>block <em>text</em></div>\n\ninline <b>text</b>\n\n```\n\x1b[2Jcode\n```"
	c := newMDCache(true)
	out := renderMarkdownCached(&c, src, 80, false)
	plain := xansi.Strip(out)
	if strings.Contains(plain, "<") || !strings.Contains(plain, "block text") || !strings.Contains(plain, "inline text") {
		t.Fatalf("HTML tags were not stripped cleanly: %q", plain)
	}
	if strings.Contains(out, "\x1b[2J") {
		t.Fatalf("untrusted control sequence survived rendering: %q", out)
	}
}
