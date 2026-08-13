package mowi

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

// Resume seeds the transcript before the first WindowSize. After layout, the
// user must be able to leave follow-bottom and scroll into earlier turns.
func TestResumeSessionCanScrollUp(t *testing.T) {
	m := newModel(testEngine(t), false, false)
	if m.ready {
		t.Fatal("seed happens before first layout")
	}
	// Long prior session: more than a screen of user/assistant turns, and
	// more than entryTextKeepFull so a premature GC would stub the head.
	for i := 0; i < 60; i++ {
		m.addAt(kindUser, fmt.Sprintf("user turn %02d unique-marker-%02d", i, i), time.Time{})
		m.addAt(kindAssistant, strings.Repeat(fmt.Sprintf("answer-%02d ", i), 8), time.Time{})
	}
	m.add(kindStatus, "resumed · 60 turns")
	m.showWelcome = false
	m.followBottom = true

	mod, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = mod.(*model)
	if !m.ready {
		t.Fatal("window size should ready the viewport")
	}
	if m.vp.TotalLineCount() <= m.vp.VisibleLineCount() {
		t.Fatalf("fixture not tall enough to scroll: total=%d vis=%d",
			m.vp.TotalLineCount(), m.vp.VisibleLineCount())
	}
	yBottom := m.vp.YOffset()
	if !m.followBottom || yBottom == 0 {
		t.Fatalf("resume should pin to bottom: follow=%v y=%d", m.followBottom, yBottom)
	}

	// Half-page up (ctrl+u default).
	mod, _ = m.Update(tea.KeyPressMsg{Mod: tea.ModCtrl, Code: 'u'})
	m = mod.(*model)
	if m.followBottom {
		t.Fatal("scroll-up after resume left followBottom pinned")
	}
	if m.vp.YOffset() >= yBottom {
		t.Fatalf("scroll-up did not move viewport: y=%d bottom=%d total=%d vis=%d",
			m.vp.YOffset(), yBottom, m.vp.TotalLineCount(), m.vp.VisibleLineCount())
	}

	// Wheel-up should keep moving, not snap back to the resume banner.
	before := m.vp.YOffset()
	mod, _ = m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelUp})
	m = mod.(*model)
	if m.followBottom {
		t.Fatal("wheel-up re-armed followBottom")
	}
	if m.vp.YOffset() >= before {
		t.Fatalf("wheel-up did not scroll: before=%d after=%d", before, m.vp.YOffset())
	}

	// Early turns must still be real text — seed used to GC them before the
	// first layout, so scrolling up only showed stub markers.
	if m.entries[0].gc || !strings.Contains(m.entries[0].text, "unique-marker-00") {
		t.Fatalf("head turn was stubbed before scroll: gc=%v text=%q", m.entries[0].gc, m.entries[0].text)
	}
}
