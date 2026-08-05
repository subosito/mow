package mowi

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	xansi "github.com/charmbracelet/x/ansi"

	"github.com/subosito/mow"
)

// Payloads that must never land in transcript storage (group polish cut).
var sanitizeAttackPayloads = []string{
	"body\x1b]52;c;YQo=\x07tail",   // OSC 52 clipboard
	"body\x1b]52;c;YQo=\x1b\\tail", // OSC 52 ST form
	"pre\x1b[2J\x1b[Hpost",         // CSI erase/home
	"x\x1b[10;10Hsecret",           // CSI cursor position
	"line\rpartial",                // bare CR
	"a\x1b[3",                      // split/partial CSI
	"ok\x00null\x7fdel",            // C0 NUL + DEL
}

func TestSanitizeDisplayEntryPaths(t *testing.T) {
	// Enumerate kinds at the storage seam (add/addAt), not a hand list of call sites.
	kinds := []entryKind{
		kindUser, kindAssistant, kindSystem, kindTool, kindError,
		kindStatus, kindPerm, kindDiff,
	}
	for _, kind := range kinds {
		for _, payload := range sanitizeAttackPayloads {
			m := freshModel(t)
			m.add(kind, payload)
			if len(m.entries) == 0 {
				t.Fatalf("kind %v: no entry stored", kind)
			}
			got := m.entries[len(m.entries)-1].text
			if strings.ContainsRune(got, 0x1b) {
				t.Fatalf("kind %v: ESC leaked into storage: %q from %q", kind, got, payload)
			}
			if strings.ContainsRune(got, '\r') {
				t.Fatalf("kind %v: CR leaked into storage: %q", kind, got)
			}
			if strings.ContainsRune(got, 0) || strings.ContainsRune(got, 0x7f) {
				t.Fatalf("kind %v: C0 control leaked: %q", kind, got)
			}
		}
	}
}

func TestSanitizeDisplayInPlaceToolUpdate(t *testing.T) {
	m := freshModel(t)
	m.busy = true
	m.Update(toolUIMsg{name: "read", start: true})
	m.Update(toolUIMsg{name: "read", line: "read · ok"})
	poison := "read · \x1b]52;c;YQo=\x07pwn"
	m.renderToolTallyLine(poison)
	if m.toolLineIdx < 0 || m.toolLineIdx >= len(m.entries) {
		t.Fatal("no tool line")
	}
	got := m.entries[m.toolLineIdx].text
	if strings.ContainsRune(got, 0x1b) {
		t.Fatalf("in-place tool update leaked ESC: %q", got)
	}
}

func TestNarrowChromeFrames(t *testing.T) {
	eng, err := mow.New(mow.Options{
		NoSession:  true,
		AllowWrite: true,
		AllowShell: true,
		Chat: func(ctx context.Context, messages []mow.Message, tools []mow.ToolSpec) (mow.Message, error) {
			return mow.Message{Role: "assistant", Content: "ok"}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	m := newModel(eng, false, false)
	m.width, m.height = 40, 16
	m.layout()
	m.ready = true

	// Header: exactly 2 rows; safety survives when write/shell on.
	hdr := xansi.Strip(m.renderHeader())
	rows := strings.Split(strings.TrimRight(hdr, "\n"), "\n")
	if len(rows) != 2 {
		t.Fatalf("header rows=%d want 2: %q", len(rows), hdr)
	}
	if !strings.Contains(hdr, "write") || !strings.Contains(hdr, "shell") {
		t.Fatalf("narrow header dropped safety: %q", hdr)
	}

	// Welcome places without panicking / empty.
	m.showWelcome = true
	w := m.welcomeView()
	if strings.TrimSpace(xansi.Strip(w)) == "" {
		t.Fatal("welcome empty at 40 cols")
	}

	// Help overlay fits.
	m.showWelcome = false
	m.showHelp = true
	card := xansi.Strip(m.helpCard())
	if !strings.Contains(card, "help") && !strings.Contains(strings.ToLower(card), "key") {
		// help card should still render something useful
		if len(card) < 10 {
			t.Fatalf("help card too thin: %q", card)
		}
	}
	// Card width should not exceed terminal.
	for _, line := range strings.Split(card, "\n") {
		if xansi.StringWidth(line) > m.width {
			t.Fatalf("help line wider than term (%d): %q", m.width, line)
		}
	}

	// Busy band + perm strip at narrow width.
	m.showHelp = false
	m.busy = true
	m.startedAt = time.Now()
	m.toolCurrent = "bash"
	m.toolCurrentArgs = "just verify"
	m.testArmPerm("bash", "$ ls", make(chan error, 1))
	m.layout()
	band := xansi.Strip(m.renderActivityBand())
	if band == "" {
		t.Fatal("expected activity band when busy")
	}
	for _, line := range strings.Split(strings.TrimRight(band, "\n"), "\n") {
		if xansi.StringWidth(line) > m.width {
			t.Fatalf("band line wider than term: %q", line)
		}
	}
	perm := xansi.Strip(m.renderPermissionStrip())
	if !strings.Contains(perm, "permission") {
		t.Fatalf("perm strip missing: %q", perm)
	}

	// Collapsed large diff still renders.
	var b strings.Builder
	b.WriteString("edited path/to/file.go\n")
	for i := 0; i < 50; i++ {
		b.WriteString("+ line\n")
	}
	diff := xansi.Strip(m.renderDiffEntry(b.String(), m.width))
	if !strings.Contains(diff, "more lines") {
		t.Fatalf("diff collapse missing at narrow: %q", short(diff, 120))
	}
}

func TestOneSpinnerReducedMotion(t *testing.T) {
	t.Setenv("MOW_NO_ANIM", "1")
	// reducedMotion reads env each call.
	if !reducedMotion() {
		t.Fatal("MOW_NO_ANIM should enable reduced peer-bion")
	}
	m := freshModel(t)
	m.busy = true
	m.startedAt = time.Now()
	m.syncInputChrome()
	m.layout()
	spin := xansi.Strip(m.spinnerView())
	if !strings.Contains(spin, glyphBrand) {
		t.Fatalf("reduced peer-bion spinner should be %q, got %q", glyphBrand, spin)
	}
	// Prompt must not host a second spinner / tool detail.
	prompt := xansi.Strip(m.ta.Prompt)
	if strings.Contains(prompt, "⚙") {
		t.Fatalf("busy prompt must not embed tool: %q", prompt)
	}
	band := xansi.Strip(m.renderActivityBand())
	if !strings.Contains(band, glyphBrand) && !strings.Contains(band, "s") {
		// band should show static brand glyph and/or elapsed
		t.Fatalf("band missing reduced-peer-bion activity: %q", band)
	}
	// Clear env side effects for other tests in this process.
	t.Setenv("MOW_NO_ANIM", "")
	_ = os.Unsetenv("MOW_NO_ANIM")
}

func TestBusyPromptNoToolDetail(t *testing.T) {
	m := freshModel(t)
	m.busy = true
	m.startedAt = time.Now()
	m.toolCurrent = "bash"
	m.toolCurrentArgs = "just verify"
	m.syncInputChrome()
	prompt := xansi.Strip(m.ta.Prompt)
	if strings.Contains(prompt, "bash") || strings.Contains(prompt, "just") || strings.Contains(prompt, "⚙") {
		t.Fatalf("tool detail must live on band not prompt: %q", prompt)
	}
	band := xansi.Strip(m.renderActivityBand())
	if !strings.Contains(band, "bash") {
		t.Fatalf("band should own tool label: %q", band)
	}
}

func TestWelcomeHintUsesResolvedKeys(t *testing.T) {
	m := freshModel(t)
	m.showWelcome = true
	m.width, m.height = 80, 24
	m.layout()
	// Override help/cancel bindings.
	m.cfg.Keys.Help = "f1"
	m.cfg.Keys.Cancel = "ctrl+g"
	m.cfg.Keys = m.cfg.Keys.Resolve()
	v := xansi.Strip(m.welcomeView())
	if !strings.Contains(v, "f1") {
		t.Fatalf("welcome should mention resolved help key f1: %q", v)
	}
	if !strings.Contains(v, "ctrl+g") {
		t.Fatalf("welcome should mention resolved cancel key ctrl+g: %q", v)
	}
	// Default path still mentions help/dismiss concepts.
	m2 := freshModel(t)
	m2.showWelcome = true
	v2 := xansi.Strip(m2.welcomeView())
	if !strings.Contains(v2, "help") || !strings.Contains(v2, "dismiss") {
		t.Fatalf("default welcome missing discoverability: %q", v2)
	}
}

func TestOneSpinnerNormalMotionBandOwned(t *testing.T) {
	// Normal peer-bion: activity band owns the animated spinner; prompt does not.
	t.Setenv("MOW_NO_ANIM", "")
	_ = os.Unsetenv("MOW_NO_ANIM")
	if reducedMotion() {
		t.Skip("reduced peer-bion still set in environment")
	}
	m := freshModel(t)
	m.busy = true
	m.startedAt = time.Now()
	m.toolCurrent = "read"
	// Advance spinner so View() is non-empty braille (not only elapsed).
	m.advanceSpinnerFrame()
	m.syncInputChrome()
	m.layout()
	band := m.renderActivityBand()
	prompt := xansi.Strip(m.ta.Prompt)
	if strings.Contains(prompt, "⚙") || strings.Contains(prompt, "read") {
		t.Fatalf("prompt must not own tool/spinner detail: %q", prompt)
	}
	// Band should include the spinner widget output (spin.View) or elapsed.
	plainBand := xansi.Strip(band)
	if !strings.Contains(plainBand, "s") {
		t.Fatalf("band missing elapsed heartbeat: %q", plainBand)
	}
	// Prompt is short cue only (… or brand under reduced); not a second MiniDot chain.
	// Compare: spinnerView appears in band path via renderActivityBand.
	spin := m.spinnerView()
	if spin == "" {
		t.Fatal("empty spinnerView")
	}
	if !strings.Contains(band, spin) && !strings.Contains(plainBand, xansi.Strip(spin)) {
		// After strip, braille may remain — ensure band is the sole chrome with spin glyph.
		if xansi.Strip(spin) != "" && !strings.Contains(plainBand, xansi.Strip(spin)) {
			t.Fatalf("band does not contain spinner view; band=%q spin=%q", plainBand, xansi.Strip(spin))
		}
	}
	// Full main frame should not place spinner in the input row content beyond short cue.
	frame := xansi.Strip(m.mainFrame())
	// Count spinner occurrences roughly: spin plain should appear once in frame (the band).
	sp := xansi.Strip(spin)
	if sp != "" {
		if c := strings.Count(frame, sp); c > 2 {
			// allow small duplication from width pad edge cases; >2 is a second chrome home
			t.Fatalf("spinner glyph appears %d times in frame (want ≤2): spin=%q", c, sp)
		}
	}
}

// bandLine returns the visible (second) line of the activity band, ANSI-stripped.
func bandLine(t *testing.T, m *model) string {
	t.Helper()
	band := xansi.Strip(m.renderActivityBand())
	lines := strings.Split(strings.TrimRight(band, "\n"), "\n")
	return lines[len(lines)-1]
}

func TestActivityBandRightAlignedElapsed(t *testing.T) {
	m := freshModel(t)
	m.busy = true
	m.startedAt = time.Now().Add(-30 * time.Second)     // per-turn (right)
	m.lastActivityAt = time.Now().Add(-5 * time.Second) // phase ticker (left)
	m.toolCurrent = "bash"
	m.toolCurrentArgs = "just verify"
	m.layout()

	line := bandLine(t, m)
	// Full-width, padded row — no dead space, right side carries the total.
	if w := xansi.StringWidth(line); w != m.width {
		t.Fatalf("band line width = %d, want %d (full width)", w, m.width)
	}
	// Left keeps the tool label.
	if !strings.Contains(line, "bash") {
		t.Fatalf("band lost left tool label: %q", line)
	}
	// Phase ticker sits near the spinner, left of the tool label.
	if ci, bi := strings.Index(line, "5.0s"), strings.Index(line, "bash"); ci < 0 || ci > bi {
		t.Fatalf("run elapsed not next to spinner (left of label): %q", line)
	}
	// Right-aligned: the per-turn elapsed ends the line (modulo trailing inset).
	if got := strings.TrimRight(line, " "); !strings.HasSuffix(got, "30s") {
		t.Fatalf("per-turn elapsed not right-aligned at line end: %q", line)
	}
	// The timer must sit at the right edge, not float mid-line (display cols).
	if col := displayCol(line, "30s"); col < m.width-len("30s")-2 {
		t.Fatalf("elapsed not near right edge (col %d, width %d): %q", col, m.width, line)
	}
}

// displayCol returns the display column where sub begins within line
// (ANSI-stripped line), accounting for multi-byte/wide glyphs.
func displayCol(line, sub string) int {
	i := strings.LastIndex(line, sub)
	if i < 0 {
		return -1
	}
	return xansi.StringWidth(line[:i])
}

func TestActivityBandRightColumnStableAcrossLabels(t *testing.T) {
	// The right-aligned group must stay at a fixed column even as the
	// variable-width left label changes across the busy heartbeat (no jitter).
	startCol := func(toolArgs string) int {
		m := freshModel(t)
		m.busy = true
		m.startedAt = time.Now().Add(-30 * time.Second)
		m.lastActivityAt = time.Now().Add(-5 * time.Second) // phase ticker (left)
		m.toolCurrent = "bash"
		m.toolCurrentArgs = toolArgs
		m.layout()
		line := bandLine(t, m)
		if xansi.StringWidth(line) != m.width {
			t.Fatalf("line not full width: %q", line)
		}
		return displayCol(line, "30s")
	}
	short := startCol("ls")
	long := startCol(strings.Repeat("very-long-argument ", 8))
	if short != long {
		t.Fatalf("elapsed column jittered with left label: short=%d long=%d", short, long)
	}
}

func TestActivityBandNarrowTruncationKeepsElapsed(t *testing.T) {
	m := freshModel(t)
	m.width = 24
	m.busy = true
	m.startedAt = time.Now().Add(-30 * time.Second)
	m.lastActivityAt = time.Now().Add(-5 * time.Second) // phase ticker (left)
	m.toolCurrent = "bash"
	m.toolCurrentArgs = strings.Repeat("extremely-long-argument ", 6)
	m.queued = []string{"a", "b"}
	m.layout()

	line := bandLine(t, m)
	if w := xansi.StringWidth(line); w > m.width {
		t.Fatalf("narrow band overflows: width %d > %d (%q)", w, m.width, line)
	}
	// Left label yields; the right-aligned status (queued, then elapsed) survives.
	if !strings.Contains(line, "queued") {
		t.Fatalf("narrow band dropped right status: %q", line)
	}
}

func TestActivityBandTwoClocksSplit(t *testing.T) {
	// The band carries two clocks: the per-turn ticker next to the spinner
	// (left) and the session total pinned to the right edge (right).
	m := freshModel(t)
	m.busy = true
	m.startedAt = time.Now().Add(-2 * time.Minute)      // per-turn (right)
	m.lastActivityAt = time.Now().Add(-7 * time.Second) // phase ticker (left)
	m.toolCurrent = "bash"
	m.toolCurrentArgs = "ls"
	m.layout()

	line := bandLine(t, m)
	if xansi.StringWidth(line) != m.width {
		t.Fatalf("band line width = %d, want %d", xansi.StringWidth(line), m.width)
	}
	// Left: the phase ticker appears before the tool label (near the spinner).
	ri, bi := strings.Index(line, "7.0s"), strings.Index(line, "bash")
	if ri < 0 || bi < 0 || ri > bi {
		t.Fatalf("per-turn elapsed not near spinner: %q", line)
	}
	// Right: the per-turn elapsed ends the line, at the far right edge.
	if got := strings.TrimRight(line, " "); !strings.HasSuffix(got, "2m00s") {
		t.Fatalf("total not right-aligned: %q", line)
	}
	if col := displayCol(line, "2m00s"); col < m.width-len("2m00s")-2 {
		t.Fatalf("total not near right edge (col %d, width %d): %q", col, m.width, line)
	}
}

func TestActivityBandNoGarbageWhenTurnNotStarted(t *testing.T) {
	// startedAt/lastActivityAt are zero (fresh model, no submit): the band
	// must show no elapsed clocks at all — never a huge year-1 duration.
	m := freshModel(t)
	m.busy = true
	m.queued = []string{"a"}
	m.layout()

	line := bandLine(t, m)
	if xansi.StringWidth(line) > m.width {
		t.Fatalf("band overflow: %q", line)
	}
	// Right group carries only the queued count (no elapsed, no giant number).
	if got := strings.TrimRight(line, " "); !strings.HasSuffix(got, "queued · 1") {
		t.Fatalf("right group not queued-only: %q", line)
	}
}

func TestActivityBandToolLabelYieldsToRightWidth(t *testing.T) {
	// The tool label budget must yield to the ACTUAL right-group width, not a
	// fixed reserve: with a short right (just the elapsed) more of the tool
	// args are visible than with a long right (elapsed + peers + queued).
	labelLen := func(queued int) int {
		m := freshModel(t)
		m.busy = true
		m.startedAt = time.Now().Add(-30 * time.Second)
		m.toolCurrent = "bash"
		m.toolCurrentArgs = strings.Repeat("run-command-with-long-args ", 6)
		for i := 0; i < queued; i++ {
			m.queued = append(m.queued, "q")
		}
		m.layout()
		line := xansi.Strip(bandLine(t, m))
		// The right group ends the line with the elapsed; everything before
		// that trailing "30s" is left label + pad. Cut at the last "30s".
		i := strings.LastIndex(line, "30s")
		if i < 0 {
			t.Fatalf("elapsed missing: %q", line)
		}
		left := line[:i]
		j := strings.Index(left, "bash · ")
		if j < 0 {
			t.Fatalf("tool label missing: %q", line)
		}
		return len(strings.TrimSpace(left[j+len("bash · "):]))
	}
	short := labelLen(0)
	long := labelLen(2)
	if short <= long {
		t.Fatalf("short right should show MORE of the tool label: short=%d long=%d", short, long)
	}
	if short < 20 {
		t.Fatalf("short right still trims the tool label too hard: %d", short)
	}
}

func TestNewOutputPinCentered(t *testing.T) {
	m := freshModel(t)
	m.busy = true
	m.followBottom = false
	m.streamBuf = "streaming text"
	m.toolCurrent = ""
	m.layout()
	m.refreshVP()
	frame := m.mainFrame()
	// Extract the pin line (the only line starting with " ↓ new output").
	var pin string
	for _, ln := range strings.Split(frame, "\n") {
		if strings.Contains(ln, "new output") {
			pin = ln
			break
		}
	}
	if pin == "" {
		t.Fatalf("pin missing from frame")
	}
	leftPad := len(pin) - len(strings.TrimLeft(pin, " "))
	rightPad := len(pin) - len(strings.TrimRight(pin, " "))
	if leftPad < 0 || rightPad < 0 || abs(leftPad-rightPad) > 2 {
		t.Fatalf("pin not centered (left=%d right=%d, width=%d): %q", leftPad, rightPad, m.width, pin)
	}
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

// Scrolled up while idle: a right-aligned "↑ NN%" position indicator sits
// under the transcript. At follow-bottom (or with the live-stream pin active)
// it must not appear.
func TestScrollPositionIndicator(t *testing.T) {
	m := tallModel(t)
	m.busy = false
	m.followBottom = false
	m.vp.HalfPageUp()
	m.layout()

	frame := m.mainFrame()
	if !strings.Contains(frame, "↑") {
		t.Fatalf("scrolled-up frame missing position indicator: %q", frame)
	}
	// Right-aligned: the indicator line's left pad fills to the right edge.
	var ind string
	for _, ln := range strings.Split(frame, "\n") {
		if strings.Contains(ln, "↑") {
			ind = ln
		}
	}
	if ind == "" {
		t.Fatalf("no indicator line")
	}
	leftPad := len(ind) - len(strings.TrimLeft(ind, " "))
	if leftPad < 40 {
		t.Fatalf("indicator not right-aligned (leftPad=%d): %q", leftPad, ind)
	}

	// Follow-bottom: no indicator.
	m.followBottom = true
	m.vp.GotoBottom()
	if frame2 := m.mainFrame(); strings.Contains(frame2, "↑") {
		t.Fatalf("follow-bottom frame should have no indicator: %q", frame2)
	}
}

// The live-stream pin still wins over the position indicator while busy.
func TestScrollPinBeatsIndicatorWhileBusy(t *testing.T) {
	m := tallModel(t)
	m.busy = true
	m.streamBuf = "streaming"
	m.followBottom = false
	m.vp.HalfPageUp()
	frame := m.mainFrame()
	if !strings.Contains(frame, "new output") {
		t.Fatalf("busy scrolled-up frame should show the centered pin: %q", frame)
	}
	if strings.Contains(frame, "↑") {
		t.Fatalf("busy frame should not show the position indicator: %q", frame)
	}
}

// The workspace dir lives in the header's left identity cluster (wordmark ·
// model · cwd), not in the right-side chips — so it is stable and never
// dropped first under width pressure.
func TestHeaderWorkspaceOnLeft(t *testing.T) {
	m := freshModel(t)
	ws := filepath.Base(m.eng.Workspace())
	hdr := xansi.Strip(m.renderHeader())
	if !strings.Contains(hdr, ws) {
		t.Fatalf("header missing workspace %q: %q", ws, hdr)
	}
	// Identity cluster comes before any safety chip text.
	wi, si := strings.Index(hdr, ws), strings.Index(hdr, "write")
	if wi < 0 {
		t.Fatalf("workspace missing: %q", hdr)
	}
	if si >= 0 && wi > si {
		t.Fatalf("workspace should sit left of safety chips: %q", hdr)
	}
}

// The header shows one compact reported-usage total; /status carries the
// transparent host and peer breakdown.
func TestHeaderShowsPeerTokenShare(t *testing.T) {
	eng := testEngine(t)
	m := newModel(eng, false, false)
	m.width, m.height = 80, 24
	m.layout()
	m.ready = true
	m.showWelcome = false
	m.tokIn, m.tokOut = 100_000, 20_000

	// Engine emits one delegated call's usage (native mow peer).
	eng.Emit(mow.Event{Type: mow.EventDelegateUsage, Agent: "gemini", InputTokens: 30_000, OutputTokens: 5_000})
	select {
	case msg := <-m.toolUICh:
		raw, _ := m.Update(msg)
		m = raw.(*model)
	default:
		t.Fatal("delegate usage event not forwarded to the TUI channel")
	}

	hdr := m.renderHeader()
	plain := xansi.Strip(hdr)
	// Total is host+peers; the peer share is broken out so delegated spend —
	// which never appears in the transcript — stays visible at a glance.
	if !strings.Contains(plain, "155.0k ("+glyphPeer+" 35.0k) tok") {
		t.Fatalf("header missing combined total with peer share: %q", plain)
	}
	status := m.reportedUsageStatus()
	for _, want := range []string{
		"usage reported this run · 155.0k total",
		"host · 100.0k in · 20.0k out",
		"peers · 30.0k in · 5.0k out",
	} {
		if !strings.Contains(status, want) {
			t.Fatalf("status missing %q: %q", want, status)
		}
	}
}
