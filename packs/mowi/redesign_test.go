package mowi

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	xansi "github.com/charmbracelet/x/ansi"
	"github.com/subosito/mow"
)

func seedTurn(m *model, q string) {
	m.eng.Prompt(context.Background(), q)
	m.add(kindUser, q)
	m.add(kindAssistant, "answer to "+q)
	m.refreshVP()
}

// /steer while a turn runs injects guidance; a plain draft still queues.
func TestSteerWhileBusy(t *testing.T) {
	m := freshModel(t)
	m.showWelcome = false
	m.busy = true
	m.ta.SetValue("/steer focus on tests")
	mod, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	mm := mod.(*model)
	if mm.ta.Value() != "" {
		t.Fatalf("input not cleared: %q", mm.ta.Value())
	}
	var marked bool
	for _, e := range mm.entries {
		if e.kind == kindStatus && strings.Contains(e.text, "steer") && strings.Contains(e.text, "focus on tests") {
			marked = true
		}
	}
	if !marked {
		t.Fatal("missing steer marker")
	}

	m2 := freshModel(t)
	m2.busy = true
	m2.ta.SetValue("plain message")
	mod2, _ := m2.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if len(mod2.(*model).queued) != 1 {
		t.Fatalf("plain draft should queue while busy: %v", mod2.(*model).queued)
	}
}

// Write/edit approvals show a real before/after diff, not a JSON blob.
func TestPermDiffPreview(t *testing.T) {
	edit := permPreview("edit", []byte(`{"path":"a.go","old_string":"x","new_string":"y"}`))
	for _, want := range []string{"a.go", "@@ replace @@", "- x", "+ y"} {
		if !strings.Contains(edit, want) {
			t.Fatalf("edit preview missing %q: %q", want, edit)
		}
	}
	write := permPreview("write", []byte(`{"path":"b.txt","content":"line1\nline2"}`))
	if !strings.Contains(write, "@@ write @@") || !strings.Contains(write, "+ line1") {
		t.Fatalf("write preview: %q", write)
	}
	if bash := permPreview("bash", []byte(`{"command":"ls -la"}`)); !strings.Contains(bash, "$ ls -la") {
		t.Fatalf("bash preview: %q", bash)
	}
}

// @path references inline workspace files into the sent prompt, jailed to the
// workspace (+ optional extra roots via mow.Engine.ResolvePath).
func TestFileRefExpansion(t *testing.T) {
	ws := t.TempDir()
	writeFile(t, filepath.Join(ws, "hello.go"), "package main\nfunc main(){}")
	if err := os.MkdirAll(filepath.Join(ws, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(ws, "sub", "x.txt"), "deep")
	extra := t.TempDir()
	writeFile(t, filepath.Join(extra, "lib.go"), "package lib\n")

	eng, err := mow.New(mow.Options{
		NoSession:  true,
		Workspace:  ws,
		ExtraRoots: []string{extra},
		Chat: func(ctx context.Context, messages []mow.Message, tools []mow.ToolSpec) (mow.Message, error) {
			return mow.Message{Role: "assistant", Content: "ok"}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	sent, att := expandFileRefs(eng, "look at @hello.go and @sub/x.txt please")
	if len(att) != 2 || !strings.Contains(sent, "package main") || !strings.Contains(sent, "deep") {
		t.Fatalf("expand failed: att=%v sent=%q", att, sent)
	}
	// Absolute path under extra root is allowed.
	libAbs := filepath.Join(extra, "lib.go")
	sent3, att3 := expandFileRefs(eng, "see @"+libAbs)
	if len(att3) != 1 || !strings.Contains(sent3, "package lib") {
		t.Fatalf("extra root: att=%v sent=%q", att3, sent3)
	}
	// Jail: `..` escapes and missing files are ignored, text unchanged.
	in := "sneaky @../../etc/passwd and @nope.go"
	if sent2, att2 := expandFileRefs(eng, in); len(att2) != 0 || sent2 != in {
		t.Fatalf("jail breach: att=%v", att2)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// /edit recalls the last prompt for editing; /retry regenerates; ↑ on an empty
// prompt recalls. All rewind the last turn so it is replaced, not stacked.
func TestRetryEditRecall(t *testing.T) {
	m := freshModel(t)
	m.showWelcome = false
	seedTurn(m, "original question")
	m.editLast()
	if m.ta.Value() != "original question" {
		t.Fatalf("edit loaded %q", m.ta.Value())
	}
	for _, e := range m.entries {
		if e.kind == kindAssistant {
			t.Fatal("assistant entry should be dropped on edit")
		}
	}

	m2 := freshModel(t)
	m2.showWelcome = false
	seedTurn(m2, "retry me")
	if cmd := m2.retryLast(); cmd == nil || !m2.busy {
		t.Fatalf("retry should start a turn: busy=%v cmd=%v", m2.busy, cmd)
	}

	m3 := freshModel(t)
	m3.showWelcome = false
	seedTurn(m3, "recall me")
	m3.ta.SetValue("")
	mod, _ := m3.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	if got := mod.(*model).ta.Value(); got != "recall me" {
		t.Fatalf("up-arrow recalled %q", got)
	}
}

// Without gateway Limits, header shows tokens only (no invented ctx%/cost).
func TestHeaderTokensWithoutGatewayLimits(t *testing.T) {
	t.Setenv("MOW_HOME", t.TempDir())
	eng, err := mow.New(mow.Options{
		NoSession: true,
		Model:     "custom-local-model",
		Chat: func(ctx context.Context, msgs []mow.Message, tools []mow.ToolSpec) (mow.Message, error) {
			return mow.Message{Role: "assistant", Content: "ok", Usage: mow.Usage{InputTokens: 100_000, OutputTokens: 50}}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := eng.Prompt(context.Background(), "hi"); err != nil {
		t.Fatal(err)
	}
	m := newModel(eng, false, false)
	m.width, m.height = 120, 10
	m.layout()
	m.ready = true
	m.showWelcome = false
	m.tokIn, m.tokOut = 100_000, 50
	hdr := xansi.Strip(m.renderHeader())
	// Token chip is just the number (no 'tok' suffix): 100050 -> 100.0k.
	if !strings.Contains(hdr, "100.0k") {
		t.Fatalf("header missing token chip: %q", hdr)
	}
	// Injected Chat has no /v1/models catalog → no speculative chips.
	if strings.Contains(hdr, "$") || strings.Contains(hdr, "% ctx") {
		t.Fatalf("header must not invent cost/ctx%% without gateway limits: %q", hdr)
	}
}

// The live-streamed answer must sit at the same line it lands on after commit —
// otherwise the transcript jumps down a line when a response finishes.
func TestStreamNoShiftOnCommit(t *testing.T) {
	lineOf := func(view, needle string) int {
		for i, ln := range strings.Split(view, "\n") {
			if strings.Contains(xansi.Strip(ln), needle) {
				return i
			}
		}
		return -1
	}
	m := newModel(testEngine(t), true, false)
	m.width, m.height = 80, 24
	m.layout()
	m.ready = true
	m.showWelcome = false
	m.add(kindUser, "my question")
	m.refreshVP()

	m.busy = true
	m.streamBuf = "the answer body here"
	m.followBottom = true
	m.paintLiveStream()
	streaming := lineOf(m.vp.View(), "answer body")

	m.commitAssistant("the answer body here")
	m.streamFrame = ""
	m.busy = false
	m.refreshVP()
	committed := lineOf(m.vp.View(), "answer body")

	if streaming < 0 || committed < 0 {
		t.Fatalf("answer not found: streaming=%d committed=%d", streaming, committed)
	}
	if streaming != committed {
		t.Fatalf("answer shifted on commit: streaming line=%d committed line=%d", streaming, committed)
	}
}

// The permission strip is safety-critical — it must spell out the affordances
// (not just "y / n / a") and name the tool being gated.
func TestPermissionStripAffordances(t *testing.T) {
	m := freshModel(t)
	if s := m.renderPermissionStrip(); s != "" {
		t.Fatalf("idle strip should be empty: %q", s)
	}
	m.testArmPerm("bash", "$ ls", make(chan error, 1))
	s := m.renderPermissionStrip()
	for _, want := range []string{"permission", "bash", "allow", "deny", "always", glyphWarn} {
		if !strings.Contains(s, want) {
			t.Fatalf("permission strip missing %q: %q", want, s)
		}
	}
	// The strip must show what is being approved (the args preview), not just
	// the tool name — otherwise a user could approve an unread command.
	if !strings.Contains(s, "$ ls") {
		t.Fatalf("permission strip missing args preview: %q", s)
	}
}

// The default welcome is branded with live context, not just "type a message".
func TestWelcomeShowsTaglineAndContext(t *testing.T) {
	m := freshModel(t)
	m.showWelcome = true
	v := m.welcomeView()
	for _, want := range []string{"mowi", "agentic coding", "dismiss"} {
		if !strings.Contains(v, want) {
			t.Fatalf("welcome missing %q: %q", want, short(v, 200))
		}
	}
}

// theme.name accepts a chroma style (splash catalog): the frame palette derives
// from it, light/dark is detected, and code defaults to the same style.
func TestThemeNameFromChromaStyle(t *testing.T) {
	mocha := newThemeFrom(ThemeConfig{Name: "catppuccin-mocha"}, false)
	if mocha.name != "catppuccin-mocha" {
		t.Fatalf("name=%q", mocha.name)
	}
	// Catppuccin Mocha is a dark style regardless of the terminal hint.
	if !mocha.mdDark {
		t.Fatal("catppuccin-mocha should be detected dark")
	}
	// Palette derived from the style (Text #cdd6f4, Mauve accent #cba6f7).
	if !strings.EqualFold(mocha.palette.fg, "#cdd6f4") {
		t.Fatalf("mocha fg=%q want #cdd6f4", mocha.palette.fg)
	}
	if mocha.palette.accent == mocha.palette.fg || mocha.palette.muted == mocha.palette.fg {
		t.Fatalf("mocha palette not differentiated: %+v", mocha.palette)
	}
	// Code stays palette-derived (quiet); opt into full chroma via theme.code.
	if mocha.chromaStyle != "" {
		t.Fatalf("mocha code=%q want empty (palette-derived, not splash-loud)", mocha.chromaStyle)
	}
	// A light chroma style is detected as light.
	if latte := newThemeFrom(ThemeConfig{Name: "catppuccin-latte"}, true); latte.mdDark {
		t.Fatal("catppuccin-latte should be detected light")
	}
	// theme.code still overrides the code highlighter independently.
	over := newThemeFrom(ThemeConfig{Name: "dracula", Code: "monokai"}, true)
	if over.chromaStyle != "monokai" {
		t.Fatalf("theme.code override=%q want monokai", over.chromaStyle)
	}
	// Unknown name falls back to default.
	if unk := newThemeFrom(ThemeConfig{Name: "no-such-theme-xyz"}, true); unk.name != "default" {
		t.Fatalf("unknown name=%q want default", unk.name)
	}
}

// The input prompt indicator shows on the first line only; continuation lines
// of a multi-line message align under it, not repeat it.
func TestPromptIndicatorFirstLineOnly(t *testing.T) {
	m := freshModel(t)
	m.showWelcome = false
	m.ta.SetValue("line one\nline two\nline three")
	m.syncInputChrome()
	m.syncInputHeight()
	rows := strings.Split(xansi.Strip(m.renderInput()), "\n")
	// rows[0] is the top rule; the prompt glyph must appear once, on the first
	// input row, and not on later ones.
	prompt := strings.TrimSpace(m.cfg.PromptPrefix())
	first := -1
	count := 0
	for i, r := range rows {
		if strings.Contains(r, prompt) {
			count++
			if first < 0 {
				first = i
			}
		}
	}
	if count != 1 {
		t.Fatalf("prompt %q should appear on exactly one line, got %d:\n%s", prompt, count, strings.Join(rows, "\n"))
	}
	if !strings.Contains(rows[first], "line one") {
		t.Fatalf("prompt should be on the first input line: %q", rows[first])
	}
}

func TestParseBtw(t *testing.T) {
	cases := []struct {
		in      string
		wantArg string
		wantOK  bool
	}{
		{"/btw how does X work", "how does X work", true},
		{"/btw   spaced  ", "spaced", true},
		{"/btw", "", true},
		{"/btweird not a command", "", false}, // needs space or exact
		{"/model", "", false},
		{"btw no slash", "", false},
	}
	for _, c := range cases {
		arg, ok := parseBtw(c.in)
		if ok != c.wantOK || arg != c.wantArg {
			t.Errorf("parseBtw(%q)=(%q,%v) want (%q,%v)", c.in, arg, ok, c.wantArg, c.wantOK)
		}
	}
}

// /btw routes to an ephemeral turn: it marks the aside and starts a turn
// (rather than being swallowed by the generic slash handler).
func TestBtwRoutesToEphemeralTurn(t *testing.T) {
	m := freshModel(t)
	m.showWelcome = false
	m.ta.SetValue("/btw quick question")
	mod, cmd := m.submit()
	mm := mod.(*model)
	if !mm.busy {
		t.Fatal("/btw should start a turn (busy)")
	}
	if cmd == nil {
		t.Fatal("/btw should return a run command")
	}
	// A status marker precedes the user aside.
	var sawMarker, sawUser bool
	for _, e := range mm.entries {
		if e.kind == kindStatus && strings.Contains(e.text, "btw") {
			sawMarker = true
		}
		if e.kind == kindUser && strings.Contains(e.text, "quick question") {
			sawUser = true
		}
	}
	if !sawMarker {
		t.Fatal("/btw should add a 'btw' aside marker")
	}
	if !sawUser {
		t.Fatal("/btw should add the question as a user entry")
	}
}

// /search finds transcript entries and cycles through matches.
func TestTranscriptSearch(t *testing.T) {
	m := freshModel(t)
	m.showWelcome = false
	m.add(kindUser, "how do widgets work")
	m.add(kindAssistant, "widgets are gadgets")
	m.add(kindUser, "and gizmos")
	m.add(kindAssistant, "gizmos too")
	m.refreshVP()

	m.doSearch("gizmo")
	if len(m.searchHits) != 2 || m.searchTerm != "gizmo" || m.searchIdx != 0 {
		t.Fatalf("hits=%v term=%q idx=%d", m.searchHits, m.searchTerm, m.searchIdx)
	}
	m.doSearch("") // cycle to next match
	if m.searchIdx != 1 {
		t.Fatalf("cycle idx=%d want 1", m.searchIdx)
	}
	m.doSearch("") // wrap
	if m.searchIdx != 0 {
		t.Fatalf("wrap idx=%d want 0", m.searchIdx)
	}
	m.doSearch("nomatch")
	if len(m.searchHits) != 0 {
		t.Fatalf("expected no hits: %v", m.searchHits)
	}
}

// Help groups keys and commands under section headers.
func TestHelpCardGrouped(t *testing.T) {
	m := freshModel(t)
	card := m.helpCard()
	for _, want := range []string{"KEYS", "COMMANDS", "/model", "/sessions", "/goal", "/review", "/sec"} {
		if !strings.Contains(card, want) {
			t.Fatalf("help card missing %q", want)
		}
	}
}

// A long command must yield before the permission actions: the decision keys
// stay together on one row rather than wrapping below an unread preview.
func TestPermissionStripPinsDecisionKeys(t *testing.T) {
	m := freshModel(t)
	m.width = 72
	m.testArmPerm("bash", "$ "+strings.Repeat("very-long-argument ", 12), make(chan error, 1))
	strip := xansi.Strip(m.renderPermissionStrip())
	if rows := strings.Split(strip, "\n"); len(rows) != 1 {
		t.Fatalf("permission strip wrapped into %d rows: %q", len(rows), strip)
	}
	if w := lipgloss.Width(strip); w > m.width {
		t.Fatalf("permission strip wider than terminal: %d > %d: %q", w, m.width, strip)
	}
	for _, want := range []string{"y allow", "n deny", "a always"} {
		if !strings.Contains(strip, want) {
			t.Fatalf("permission strip lost action %q: %q", want, strip)
		}
	}
}
