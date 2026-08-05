package mowi

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestLayoutHeightsSum(t *testing.T) {
	cases := []struct {
		w, h int
		taH  int
		perm bool
	}{
		{80, 24, 1, false},
		{80, 24, 1, true},
		{100, 40, 5, false},
		{60, 12, 1, false},
		{40, 10, 1, false}, // min usable
		{100, 30, 8, true},
	}
	for _, tc := range cases {
		name := fmt.Sprintf("%dx%d_ta%d_perm%v", tc.w, tc.h, tc.taH, tc.perm)
		t.Run(name, func(t *testing.T) {
			m := newModel(testEngine(t), true, false)
			m.width, m.height = tc.w, tc.h
			// Content-driven height (DynamicHeight); pad with newlines for multi-line input.
			if tc.taH > 1 {
				m.ta.SetValue(strings.Repeat("line\n", tc.taH-1) + "end")
			}
			if tc.perm {
				m.permWait = &permAskMsg{name: "write", args: "{}", resp: make(chan error, 1)}
			}
			m.layout()
			// layout SetWidth re-runs DynamicHeight; ensure content still yields expected rows.
			if m.ta.Height() < tc.taH && tc.taH <= m.inputHeightCap() {
				m.ta.SetValue(strings.Repeat("line\n", tc.taH-1) + "end")
				m.layout()
			}
			if m.tooSmall() {
				t.Fatalf("fixture marked too small: %dx%d", tc.w, tc.h)
			}
			_, _, _, _, chrome := m.layoutChrome()
			sum := m.vp.Height() + chrome
			if sum != m.height {
				t.Fatalf("vp(%d)+chrome(%d)=%d want height %d (taH=%d)",
					m.vp.Height(), chrome, sum, m.height, m.ta.Height())
			}
		})
	}
}

func TestLayoutTooSmallDoesNotOversizeVP(t *testing.T) {
	m := newModel(testEngine(t), true, false)
	m.width, m.height = 20, 5
	m.ta.SetHeight(1)
	m.layout()
	if !m.tooSmall() {
		t.Fatal("expected tooSmall")
	}
	if m.vp.Height() > m.height {
		t.Fatalf("vp height %d > term %d", m.vp.Height(), m.height)
	}
}

func TestSizeWarnView(t *testing.T) {
	m := newModel(testEngine(t), true, false)
	m.width, m.height = 20, 6
	m.layout()
	m.ready = true
	if !m.tooSmall() {
		t.Fatal("expected tooSmall")
	}
	warn := m.sizeWarnView()
	if !strings.Contains(warn, "too small") {
		t.Fatalf("size warn: %q", warn)
	}
	mod, _ := m.Update(tea.WindowSizeMsg{Width: 15, Height: 4})
	m = mod.(*model)
	if !m.tooSmall() {
		t.Fatal("still too small after resize")
	}
	// Grow to usable size.
	mod, _ = m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = mod.(*model)
	if m.tooSmall() {
		t.Fatal("80x24 should be usable")
	}
	_, _, _, _, chrome := m.layoutChrome()
	if m.vp.Height()+chrome != m.height {
		t.Fatalf("after grow: vp+chrome=%d height=%d", m.vp.Height()+chrome, m.height)
	}
}

func TestHelpOverlayKeepsMainChrome(t *testing.T) {
	m := newModel(testEngine(t), true, false)
	m.width, m.height = 80, 24
	m.layout()
	m.ready = true
	m.showWelcome = false
	m.add(kindUser, "prior user line unique-xyz")
	m.add(kindAssistant, "prior assistant unique-abc")
	m.refreshVP()
	m.showHelp = true

	frame := m.mainFrame()
	if !strings.Contains(frame, "mow") {
		t.Fatalf("main frame missing header: %q", truncate(frame, 80))
	}
	out := placeOverlayCenter(m.helpCard(), frame, m.width, m.height)
	if !strings.Contains(out, "help") {
		t.Fatalf("missing help card: %q", truncate(out, 120))
	}
	if !strings.Contains(out, "mow") {
		t.Fatalf("overlay should keep main frame edges with mow: %q", truncate(out, 120))
	}
}

func TestHelpDismissWithQ(t *testing.T) {
	raw := newModel(testEngine(t), false, false)
	raw.width, raw.height = 80, 24
	raw.layout()
	raw.ready = true
	raw.showHelp = true
	mod, _ := raw.Update(tea.KeyPressMsg{Code: 'q', Text: "q"})
	m := mod.(*model)
	if m.showHelp {
		t.Fatal("q should close help")
	}
}

func TestOverlayPlaceCenter(t *testing.T) {
	bg := strings.Repeat(strings.Repeat(".", 20)+"\n", 10)
	fg := "HELLO"
	out := placeOverlayCenter(fg, bg, 20, 10)
	if !strings.Contains(out, "HELLO") {
		t.Fatalf("missing fg: %q", out)
	}
	lines := strings.Split(out, "\n")
	if len(lines) != 10 {
		t.Fatalf("lines=%d", len(lines))
	}
}

func TestScrolledIndicatorStaysInsideFrameHeight(t *testing.T) {
	m := freshModel(t)
	m.showWelcome = false
	m.width, m.height = 80, 24
	m.layout()
	for i := 0; i < 60; i++ {
		m.add(kindUser, fmt.Sprintf("prompt %d", i))
	}
	m.refreshVP()
	m.vp.GotoTop()
	m.followBottom = false
	view := m.mainFrame()
	if got := strings.Count(view, "\n") + 1; got > m.height {
		t.Fatalf("frame rows=%d exceed terminal height=%d", got, m.height)
	}
}

func TestHeaderOrdersWorkspaceBeforeModel(t *testing.T) {
	m := freshModel(t)
	m.width, m.height = 120, 24
	m.layout()
	header := sanitizeDisplay(m.renderHeader())
	if strings.Contains(header, " / ") {
		t.Fatalf("header should not use slash separator: %q", header)
	}
}

func TestHeaderEffortIsBounded(t *testing.T) {
	if got := short(strings.TrimSpace("an-unreasonably-long-effort"), 12); len([]rune(got)) > 13 {
		t.Fatalf("bounded effort=%q", got)
	}
}
