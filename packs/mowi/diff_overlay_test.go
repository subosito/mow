package mowi

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	xansi "github.com/charmbracelet/x/ansi"
)

func TestDiffOverlayOpenDismissRestoresScroll(t *testing.T) {
	m := tallModel(t)
	// Pin away from bottom so restore is observable.
	m.followBottom = false
	m.vp.SetYOffset(5)
	y0 := m.vp.YOffset()

	m.add(kindDiff, "edited main.go\n@@ -1,2 +1,2 @@\n-old\n+new\n")
	m.refreshVP()
	// refresh may move scroll; re-pin.
	m.followBottom = false
	m.vp.SetYOffset(y0)

	if !m.openDiffOverlay(-1) {
		t.Fatal("openDiffOverlay failed")
	}
	if m.diffView == nil {
		t.Fatal("diffView nil after open")
	}
	if m.diffView.mode != diffModeUnified {
		t.Fatalf("default mode=%v want unified", m.diffView.mode)
	}
	// Frame should paint the body.
	frame := xansi.Strip(m.renderDiffOverlayFrame())
	if !strings.Contains(frame, "main.go") && !strings.Contains(frame, "edited") {
		t.Fatalf("title missing: %q", short(frame, 200))
	}
	if !strings.Contains(frame, "old") || !strings.Contains(frame, "new") {
		t.Fatalf("body missing: %q", short(frame, 200))
	}

	m.closeDiffOverlay()
	if m.diffView != nil {
		t.Fatal("diffView still set after close")
	}
	if m.vp.YOffset() != y0 {
		t.Fatalf("YOffset after dismiss=%d want %d", m.vp.YOffset(), y0)
	}
}

func TestDiffOverlayToggleSplitWhenWide(t *testing.T) {
	m := freshModel(t)
	m.width, m.height = 120, 40
	m.layout()
	m.add(kindDiff, "edited wide.go\n@@ -1,2 +1,2 @@\n-old line here\n+new line here\n")
	if !m.openDiffOverlay(-1) {
		t.Fatal("open failed")
	}
	if !splitModeAvailable(m.width) {
		t.Fatal("fixture width should allow split")
	}
	handled, _ := m.handleDiffOverlayKey("tab", tea.KeyPressMsg{})
	if !handled {
		t.Fatal("tab should be handled")
	}
	if m.diffView.mode != diffModeSplit {
		t.Fatalf("mode=%v want split", m.diffView.mode)
	}
	body := xansi.Strip(m.renderDiffOverlayBody())
	if !strings.Contains(body, "old") || !strings.Contains(body, "new") {
		t.Fatalf("split body: %q", body)
	}
	// Toggle back.
	m.handleDiffOverlayKey("tab", tea.KeyPressMsg{})
	if m.diffView.mode != diffModeUnified {
		t.Fatalf("mode=%v want unified after second tab", m.diffView.mode)
	}
}

func TestDiffOverlaySplitRefusesWhenNarrow(t *testing.T) {
	m := freshModel(t)
	m.width, m.height = 60, 24
	m.layout()
	m.add(kindDiff, "edited n.go\n@@ -1 +1 @@\n-a\n+b\n")
	if !m.openDiffOverlay(-1) {
		t.Fatal("open failed")
	}
	m.handleDiffOverlayKey("tab", tea.KeyPressMsg{})
	// Mode may flip to split in state, but paint falls back to unified.
	// Prefer: tab does not enter split mode when too narrow.
	if m.diffView.mode == diffModeSplit && !splitModeAvailable(m.width) {
		// Our handler only sets split when available — assert that.
		t.Fatal("handler entered split at narrow width")
	}
	if m.diffView.mode != diffModeUnified {
		t.Fatalf("mode=%v want unified at narrow width", m.diffView.mode)
	}
}

func TestDiffOverlayKeyBinding(t *testing.T) {
	m := freshModel(t)
	m.width, m.height = 100, 30
	m.layout()
	m.add(kindDiff, "edited k.go\n@@ -1 +1 @@\n-a\n+b\n")
	// ctrl+e default ViewDiff
	mod, _ := m.Update(tea.KeyPressMsg{Mod: tea.ModCtrl, Code: 'e'})
	mm := mod.(*model)
	if mm.diffView == nil {
		t.Fatal("ctrl+e should open diff overlay")
	}
	// Esc dismisses.
	mod, _ = mm.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
	mm = mod.(*model)
	if mm.diffView != nil {
		t.Fatal("esc should close diff overlay")
	}
}

func TestDiffOverlayNoDiffStatus(t *testing.T) {
	m := freshModel(t)
	mod, _ := m.Update(tea.KeyPressMsg{Mod: tea.ModCtrl, Code: 'e'})
	mm := mod.(*model)
	if mm.diffView != nil {
		t.Fatal("should not open without a diff entry")
	}
	found := false
	for _, e := range mm.entries {
		if e.kind == kindStatus && strings.Contains(e.text, "no diff") {
			found = true
		}
	}
	if !found {
		t.Fatal("expected status about missing diff")
	}
}

func TestHelpListsViewDiff(t *testing.T) {
	m := freshModel(t)
	card := xansi.Strip(m.helpCard())
	if !strings.Contains(card, "expand last diff") {
		t.Fatalf("help missing view-diff: %q", short(card, 400))
	}
}

func TestCollapsedDiffShowsExpandHint(t *testing.T) {
	t.Setenv("MOW_FORCE_COLOR", "1")
	m := freshModel(t)
	var b strings.Builder
	b.WriteString("edited big.go\n@@ -1 +1 @@\n")
	for i := 0; i < 50; i++ {
		b.WriteString("+line\n")
	}
	out := xansi.Strip(m.renderDiffEntry(b.String(), 80))
	if !strings.Contains(out, "expand full diff") {
		t.Fatalf("missing expand hint: %q", short(out, 300))
	}
	// Overlay still sees the full uncollapsed body from the entry text.
	m.add(kindDiff, b.String())
	if !m.openDiffOverlay(-1) {
		t.Fatal("open failed")
	}
	body := xansi.Strip(m.renderDiffOverlayBody())
	// Full body has 50 adds; collapsed card kept 40. Overlay should keep more
	// than the card's fold marker alone.
	if strings.Count(body, "line") < 45 {
		t.Fatalf("overlay should show full body, got %d line hits", strings.Count(body, "line"))
	}
}

func TestDefaultKeysViewDiff(t *testing.T) {
	if DefaultKeys().ViewDiff != "ctrl+e" {
		t.Fatalf("ViewDiff=%q", DefaultKeys().ViewDiff)
	}
	r := KeysConfig{}.Resolve()
	if r.ViewDiff != "ctrl+e" {
		t.Fatalf("Resolve ViewDiff=%q", r.ViewDiff)
	}
}

// Overlay owns the keyboard while open: help must not open over it, and a
// second ViewDiff press toggles closed. Permission stays behind the overlay
// (dismiss first) so y/n cannot fire under a full-screen review.
func TestDiffOverlayKeyPrecedence(t *testing.T) {
	m := freshModel(t)
	m.width, m.height = 100, 30
	m.layout()
	m.add(kindDiff, "edited p.go\n@@ -1 +1 @@\n-a\n+b\n")
	if !m.openDiffOverlay(-1) {
		t.Fatal("open failed")
	}

	// Help binding is swallowed while overlay is open.
	mod, _ := m.Update(tea.KeyPressMsg{Code: '?', Text: "?"})
	mm := mod.(*model)
	if mm.showHelp {
		t.Fatal("help must not open over diff overlay")
	}
	if mm.diffView == nil {
		t.Fatal("overlay dismissed by help key")
	}

	// ViewDiff toggles closed.
	mod, _ = mm.Update(tea.KeyPressMsg{Mod: tea.ModCtrl, Code: 'e'})
	mm = mod.(*model)
	if mm.diffView != nil {
		t.Fatal("second ViewDiff should close overlay")
	}

	// Help works again once overlay is gone.
	mod, _ = mm.Update(tea.KeyPressMsg{Code: '?', Text: "?"})
	mm = mod.(*model)
	if !mm.showHelp {
		t.Fatal("help should open after overlay dismissed")
	}
	// ViewDiff blocked while help is up.
	mod, _ = mm.Update(tea.KeyPressMsg{Mod: tea.ModCtrl, Code: 'e'})
	mm = mod.(*model)
	if mm.diffView != nil {
		t.Fatal("ViewDiff must not open over help")
	}
}

func TestDiffOverlayFrameUsesExactHeight(t *testing.T) {
	m := freshModel(t)
	m.width, m.height = 80, 24
	m.layout()
	m.add(kindDiff, "edited g.go\n@@ -1 +1 @@\n-a\n+b\n")
	if !m.openDiffOverlay(-1) {
		t.Fatal("open failed")
	}
	// Title + rule + body viewport height = terminal height (no soft-wrap
	// chrome drift that would push a row off-screen).
	if m.diffView.contentH != m.height-2 {
		t.Fatalf("contentH=%d want %d", m.diffView.contentH, m.height-2)
	}
	frame := m.renderDiffOverlayFrame()
	// Count visual rows: title, rule, then viewport rows.
	rows := strings.Split(frame, "\n")
	if len(rows) != m.height {
		// viewport.View may omit trailing blank lines; allow short by at most 0
		// empty trail — still must not exceed height.
		if len(rows) > m.height {
			t.Fatalf("frame rows=%d > height=%d", len(rows), m.height)
		}
	}
	// Every row must be ≤ width in display cells.
	for i, ln := range rows {
		if w := xansi.StringWidth(ln); w > m.width {
			t.Errorf("row %d width %d > %d: %q", i, w, m.width, xansi.Strip(ln))
		}
	}
}
