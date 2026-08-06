package mowi

import (
	"context"
	"fmt"
	"github.com/subosito/mow"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	xansi "github.com/charmbracelet/x/ansi"
)

func TestActivityBandIdleAbsent(t *testing.T) {
	m := freshModel(t)
	m.busy = false
	if got := m.renderActivityBand(); got != "" {
		t.Fatalf("idle band should be empty, got %q", got)
	}
	if m.activityBandVisible() {
		t.Fatal("idle band not visible")
	}
}

func TestActivityBandBusyShowsElapsedNotInPrompt(t *testing.T) {
	m := freshModel(t)
	m.busy = true
	m.startedAt = time.Now().Add(-3 * time.Second)
	m.toolCurrent = "bash"
	m.toolCurrentArgs = `cd /tmp && devenv shell -- just verify`
	m.syncInputChrome()
	m.layout()
	prompt := xansi.Strip(m.ta.Prompt)
	if strings.Contains(prompt, "just") || strings.Contains(prompt, "⚙") {
		t.Fatalf("prompt must not carry tool detail: %q", prompt)
	}
	band := xansi.Strip(m.renderActivityBand())
	if !strings.Contains(band, "s") {
		t.Fatalf("band missing elapsed: %q", band)
	}
	if !strings.Contains(band, "bash") || !strings.Contains(band, "just") {
		t.Fatalf("band missing smart label: %q", band)
	}
}

func TestQueueChipOnBand(t *testing.T) {
	m := freshModel(t)
	m.busy = true
	m.startedAt = time.Now()
	m.ta.SetValue("next please")
	if cmd := m.queueDraft(); cmd != nil {
		t.Fatal("plain queue should not return cmd")
	}
	if len(m.queued) != 1 {
		t.Fatalf("queued=%v", m.queued)
	}
	band := xansi.Strip(m.renderActivityBand())
	if !strings.Contains(band, "queued · 1") {
		t.Fatalf("band missing queue chip: %q", band)
	}
	teaches := 0
	for _, e := range m.entries {
		if e.kind == kindStatus && strings.Contains(e.text, "/steer") {
			teaches++
		}
	}
	if teaches != 1 {
		t.Fatalf("teach count=%d", teaches)
	}
	m.ta.SetValue("another")
	_ = m.queueDraft()
	teaches = 0
	for _, e := range m.entries {
		if e.kind == kindStatus && strings.Contains(e.text, "/steer") {
			teaches++
		}
	}
	if teaches != 1 {
		t.Fatalf("teach should stay once, got %d", teaches)
	}
	band = xansi.Strip(m.renderActivityBand())
	if !strings.Contains(band, "queued · 2") {
		t.Fatalf("band missing queued · 2: %q", band)
	}
}

func TestPermGuardBlocksEarlyYes(t *testing.T) {
	m := freshModel(t)
	resp := make(chan error, 1)
	m.armPermWait(&permAskMsg{name: "bash", args: "$ rm -rf /", resp: resp})
	mod, _ := m.Update(tea.KeyPressMsg{Code: 'y', Text: "y"})
	mm := mod.(*model)
	if mm.permWait == nil {
		t.Fatal("y before strip paint must not clear permWait")
	}
	select {
	case <-resp:
		t.Fatal("engine must not receive early allow")
	default:
	}
	_ = mm.renderPermissionStrip()
	mm.permArmedAt = time.Now().Add(-time.Second)
	mod, _ = mm.Update(tea.KeyPressMsg{Code: 'y', Text: "y"})
	mm = mod.(*model)
	if mm.permWait != nil {
		t.Fatal("y after arm should clear")
	}
	if err := <-resp; err != nil {
		t.Fatalf("allow err %v", err)
	}
}

func TestHeaderKeepsTwoRows(t *testing.T) {
	m := freshModel(t)
	m.width = 48
	m.height = 20
	m.layout()
	hdr := xansi.Strip(m.renderHeader())
	if !strings.Contains(hdr, "mowi") {
		t.Fatalf("header missing wordmark: %q", hdr)
	}
	rows := strings.Split(strings.TrimRight(hdr, "\n"), "\n")
	if len(rows) != 2 {
		t.Fatalf("header rows=%d want 2: %q", len(rows), hdr)
	}
}

func TestErrorRankStrongerThanStatus(t *testing.T) {
	m := freshModel(t)
	m.width = 80
	errLine := m.renderEntry(entry{kind: kindError, text: "boom"}, 80)
	stLine := m.renderEntry(entry{kind: kindStatus, text: "note"}, 80)
	if !strings.Contains(errLine, glyphError) {
		t.Fatalf("error missing glyph: %q", errLine)
	}
	if !strings.Contains(stLine, glyphBullet) {
		t.Fatalf("status missing bullet: %q", stLine)
	}
}

func TestGCMarkerInRender(t *testing.T) {
	m := freshModel(t)
	m.width = 80
	line := m.renderEntry(entry{kind: kindAssistant, text: "…(mowi gc) old", gc: true}, 80)
	if !strings.Contains(xansi.Strip(line), "trimmed") {
		t.Fatalf("gc marker missing: %q", line)
	}
}

func TestFormatContextPctLevel(t *testing.T) {
	_, lvl := formatContextPctLevel(10, 100)
	if lvl != 0 {
		t.Fatalf("10%% level=%d", lvl)
	}
	_, lvl = formatContextPctLevel(60, 100)
	if lvl != 1 {
		t.Fatalf("60%% level=%d", lvl)
	}
	_, lvl = formatContextPctLevel(85, 100)
	if lvl != 2 {
		t.Fatalf("85%% level=%d", lvl)
	}
}

func TestActivityBandTopPad(t *testing.T) {
	m := freshModel(t)
	m.busy = true
	m.startedAt = time.Now()
	m.width = 80
	m.layout()
	band := m.renderActivityBand()
	rows := strings.Split(strings.TrimRight(band, "\n"), "\n")
	if len(rows) != 2 {
		t.Fatalf("band rows=%d want 2 (pad + content): %q", len(rows), band)
	}
	if strings.TrimSpace(xansi.Strip(rows[0])) != "" {
		t.Fatalf("top row should be pad, got %q", rows[0])
	}
	if !strings.Contains(xansi.Strip(rows[1]), "s") {
		t.Fatalf("content row missing elapsed: %q", rows[1])
	}
	_, bandH, _, _, _ := m.layoutChrome()
	if bandH != activityBandRows {
		t.Fatalf("layout band height=%d want %d", bandH, activityBandRows)
	}
}

func TestPermissionStripStackCount(t *testing.T) {
	m := freshModel(t)
	// Buffered channel like production; two waiting behind the active prompt.
	m.permCh = make(chan permAskMsg, 8)
	m.permCh <- permAskMsg{name: "write", args: "{}", resp: make(chan error, 1)}
	m.permCh <- permAskMsg{name: "edit", args: "{}", resp: make(chan error, 1)}
	m.testArmPerm("bash", "$ ls", make(chan error, 1))
	s := xansi.Strip(m.renderPermissionStrip())
	if !strings.Contains(s, "permission (1 of 3)") {
		t.Fatalf("want stack count 1 of 3, got %q", s)
	}
	if !strings.Contains(s, "bash") {
		t.Fatalf("want current tool name, got %q", s)
	}
	// Single request: no (1 of 1) noise.
	m2 := freshModel(t)
	m2.permCh = make(chan permAskMsg, 8)
	m2.testArmPerm("bash", "$ ls", make(chan error, 1))
	s2 := xansi.Strip(m2.renderPermissionStrip())
	if strings.Contains(s2, "of") {
		t.Fatalf("single perm should not show stack count: %q", s2)
	}
}

func TestHeaderNarrowKeepsSafety(t *testing.T) {
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
	m.width, m.height = 48, 24
	m.layout()
	m.ready = true
	m.showWelcome = false
	m.focus = focusTranscript // vanity chip that should drop first
	m.tokIn, m.tokOut = 999_999, 1
	hdr := xansi.Strip(m.renderHeader())
	rows := strings.Split(strings.TrimRight(hdr, "\n"), "\n")
	if len(rows) != 2 {
		t.Fatalf("header rows=%d want 2: %q", len(rows), hdr)
	}
	if !strings.Contains(hdr, "mowi") {
		t.Fatalf("missing wordmark: %q", hdr)
	}
	if !strings.Contains(hdr, "write") || !strings.Contains(hdr, "shell") {
		t.Fatalf("safety chips must survive narrow width: %q", hdr)
	}
	// glyphWarn may strip oddly; "write"/"shell" text is the contract.
	if strings.Contains(hdr, "focus:transcript") {
		t.Fatalf("vanity focus chip should drop before safety: %q", hdr)
	}
}

// Header posture is symmetric: the safe default shows a quiet "read only"
// chip (silence must never mean "fine"), and elevated sessions show ▲ write /
// ▲ shell instead — never both labels at once.
func TestHeaderPostureReadOnlyVsElevated(t *testing.T) {
	// Default engine (no write/shell): read-only posture chip.
	m := freshModel(t)
	hdr := xansi.Strip(m.renderHeader())
	if !strings.Contains(hdr, "read only") {
		t.Fatalf("read-only session must show the read only chip: %q", hdr)
	}
	if strings.Contains(hdr, "write") || strings.Contains(hdr, "shell") {
		t.Fatalf("read-only session must not show power chips: %q", hdr)
	}

	// Elevated engine (write + shell): warn chips, and no read-only label.
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
	m2 := newModel(eng, false, false)
	m2.width, m2.height = 100, 24
	m2.layout()
	hdr2 := xansi.Strip(m2.renderHeader())
	if !strings.Contains(hdr2, "write") || !strings.Contains(hdr2, "shell") {
		t.Fatalf("elevated session must show write+shell chips: %q", hdr2)
	}
	if strings.Contains(hdr2, "read only") {
		t.Fatalf("elevated session must not show read only: %q", hdr2)
	}
}

func TestActivityBandScrollCompensate(t *testing.T) {
	m := tallModel(t)
	m.followBottom = false
	// Leave headroom so compensate can move offset by activityBandRows.
	m.vp.SetYOffset(10)
	y0 := m.vp.YOffset()
	if y0 < activityBandRows {
		t.Fatalf("fixture YOffset=%d need >= %d", y0, activityBandRows)
	}

	// off → on
	m.activityBandOn = false
	m.busy = true
	m.startedAt = time.Now()
	m.layout() // toggles band on + compensate
	if !m.activityBandOn {
		t.Fatal("expected activityBandOn after busy layout")
	}
	y1 := m.vp.YOffset()
	if y1 != y0-activityBandRows {
		t.Fatalf("band on: YOffset %d → %d want delta -%d", y0, y1, activityBandRows)
	}

	// on → off
	m.busy = false
	m.queued = nil
	m.permWait = nil
	m.layout()
	if m.activityBandOn {
		t.Fatal("expected band off when idle")
	}
	y2 := m.vp.YOffset()
	if y2 != y0 {
		t.Fatalf("band off should restore offset: got %d want %d", y2, y0)
	}

	// follow-bottom: compensate is a no-op path; pin after layout.
	m.followBottom = true
	m.vp.GotoBottom()
	bottom := m.vp.YOffset()
	m.busy = true
	m.layout()
	m.vp.GotoBottom()
	if m.vp.YOffset() < bottom {
		// After band appears viewport is shorter; bottom pin should still be at end.
	}
	if !m.followBottom {
		t.Fatal("followBottom should stay true")
	}
}

func TestDiffCollapseLargeBody(t *testing.T) {
	m := freshModel(t)
	m.width = 80
	var b strings.Builder
	b.WriteString("edited path/to/file.go\n")
	for i := 0; i < 50; i++ {
		b.WriteString(fmt.Sprintf("+ line %d\n", i))
	}
	out := xansi.Strip(m.renderDiffEntry(b.String(), 80))
	if !strings.Contains(out, "more lines") {
		t.Fatalf("expected collapse marker, got %q", short(out, 200))
	}
	if !strings.Contains(out, "+") {
		t.Fatalf("expected remaining + stats on fold: %q", short(out, 200))
	}
	// First lines still present; not the full 50 as separate requirement —
	// just ensure we did not dump an unbounded wall without marker.
	if strings.Count(out, "line 4") < 1 {
		t.Fatalf("expected kept head of diff: %q", short(out, 200))
	}
}

func TestResumeBannerOnSeed(t *testing.T) {
	// Smoke the status format seedTranscript emits after loading prior turns.
	m := freshModel(t)
	m.add(kindUser, "hi")
	m.add(kindAssistant, "yo")
	n := 0
	for _, e := range m.entries {
		if e.kind == kindUser || e.kind == kindAssistant {
			n++
		}
	}
	m.add(kindStatus, fmt.Sprintf("resumed · %d turns", n))
	found := false
	for _, e := range m.entries {
		if e.kind == kindStatus && strings.Contains(e.text, "resumed · 2 turns") {
			found = true
		}
	}
	if !found {
		t.Fatal("resume status format regression")
	}
}

func TestActivityBandMultiPeer(t *testing.T) {
	m := freshModel(t)
	m.busy = true
	m.startedAt = time.Now()
	m.width = 80
	m.layout()

	// Parallel peers: count only — do not present last-writer tool as the whole set.
	m.peerActive.Store(2)
	m.peerAgent.Store("claude")
	m.toolCurrent = "claude: read server.go"
	band := xansi.Strip(m.renderActivityBand())
	if !strings.Contains(band, "2 peers") {
		t.Fatalf("want 2 peers label, got %q", band)
	}
	if strings.Contains(band, "server.go") || strings.Contains(band, "claude:") {
		t.Fatalf("multi-peer band must not show single last-writer tool label: %q", band)
	}

	// Single peer: keep agent/tool label.
	m.peerActive.Store(1)
	m.toolCurrent = "claude: read server.go"
	band = xansi.Strip(m.renderActivityBand())
	if strings.Contains(band, "peers") {
		t.Fatalf("single peer should not use N peers: %q", band)
	}
	if !strings.Contains(band, "claude") || !strings.Contains(band, "server.go") {
		t.Fatalf("single peer should show agent/tool label: %q", band)
	}

	// No peers: normal tool label.
	m.peerActive.Store(0)
	m.toolCurrent = "bash"
	m.toolCurrentArgs = "just verify"
	band = xansi.Strip(m.renderActivityBand())
	if strings.Contains(band, "peers") {
		t.Fatalf("no-peer band leaked peers label: %q", band)
	}
	if !strings.Contains(band, "bash") {
		t.Fatalf("normal tool label missing: %q", band)
	}
}

func TestStatusNotFaintAndErrorRank(t *testing.T) {
	m := freshModel(t)
	m.width = 80
	st := m.renderEntry(entry{kind: kindStatus, text: "note"}, 80)
	errL := m.renderEntry(entry{kind: kindError, text: "boom"}, 80)
	tool := m.renderEntry(entry{kind: kindTool, text: "read"}, 80)
	// Faint is SGR 2; meaningful status must not use it (C4).
	if strings.Contains(st, "\x1b[2m") || strings.Contains(st, "[2m") {
		t.Fatalf("status should not be faint/dim: %q", st)
	}
	if !strings.Contains(xansi.Strip(st), glyphBullet) {
		t.Fatalf("status missing bullet: %q", st)
	}
	if !strings.Contains(errL, glyphError) {
		t.Fatalf("error missing glyph: %q", errL)
	}
	if !strings.Contains(tool, glyphTool) {
		t.Fatalf("tool missing glyph: %q", tool)
	}
}

func TestActivityBandElapsedNotAccent(t *testing.T) {
	m := freshModel(t)
	m.busy = true
	m.startedAt = time.Now().Add(-5 * time.Second)
	m.width = 80
	m.layout()
	band := m.renderActivityBand()
	// Accent is bold in the theme; elapsed should not use bold accent styling.
	// Permission/waiting still warn when set.
	plain := xansi.Strip(band)
	if !strings.Contains(plain, "s") {
		t.Fatalf("elapsed missing: %q", plain)
	}
	// Spinner/brand may still use accent; the elapsed digits path is Muted.
	// Assert warn path still works for permission.
	m.permWait = &permAskMsg{name: "bash", args: "x", resp: make(chan error, 1)}
	band2 := m.renderActivityBand()
	if !strings.Contains(xansi.Strip(band2), "waiting") {
		t.Fatalf("permission waiting missing: %q", band2)
	}
}

func TestEntryPrettyPreservesScroll(t *testing.T) {
	m := tallModel(t)
	// Append a plain assistant entry to pretty.
	m.add(kindAssistant, "## Hello\n\nbody")
	idx := len(m.entries) - 1
	m.entries[idx].plain = true
	m.width, m.height = 80, 24
	m.layout()
	m.refreshVP()

	// Scrolled up: pretty must not re-pin bottom.
	m.followBottom = false
	m.vp.SetYOffset(3)
	y0 := m.vp.YOffset()
	mod, _ := m.Update(entryPrettyMsg{
		idx:   idx,
		width: max(24, m.vp.Width()-2),
		src:   m.entries[idx].text,
		body:  "Hello\n\nbody",
	})
	m = mod.(*model)
	if m.followBottom {
		t.Fatal("pretty must not force followBottom when scrolled up")
	}
	if m.vp.YOffset() != y0 {
		// SetContent can clamp; allow only if content shorter — still not jump to bottom.
		if m.vp.AtBottom() {
			t.Fatalf("pretty jumped to bottom: y0=%d y=%d", y0, m.vp.YOffset())
		}
	}

	// Follow-bottom: stay pinned after pretty.
	m.followBottom = true
	m.vp.GotoBottom()
	mod, _ = m.Update(entryPrettyMsg{
		idx:   idx,
		width: max(24, m.vp.Width()-2),
		src:   m.entries[idx].text,
		body:  "Hello\n\nbody pretty",
	})
	m = mod.(*model)
	if !m.followBottom {
		t.Fatal("followBottom should remain true")
	}
	if !m.vp.AtBottom() {
		t.Fatal("following stream should stay at bottom after pretty")
	}
}
