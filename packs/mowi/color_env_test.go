package mowi

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	xansi "github.com/charmbracelet/x/ansi"
)

func TestNoColorStripsSGR(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	t.Setenv("MOW_FORCE_COLOR", "")
	m := newModel(testEngine(t), false, false)
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m.add(kindAssistant, "**bold** and `code`")
	m.add(kindError, "something failed")
	m.refreshVP()
	view := m.View().Content
	// No SGR color sequences (foreground/background) anywhere.
	if strings.Contains(view, "\x1b[38;") || strings.Contains(view, "\x1b[48;") {
		t.Fatalf("NO_COLOR still emitted color SGR: %q", firstColorSeq(view))
	}
}

func TestForceColorOverridesNoColor(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	t.Setenv("MOW_FORCE_COLOR", "1")
	m := newModel(testEngine(t), false, false)
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m.add(kindError, "boom")
	m.refreshVP()
	if !strings.Contains(m.View().Content, "\x1b[38;") {
		t.Fatal("MOW_FORCE_COLOR should re-enable color despite NO_COLOR")
	}
}

func firstColorSeq(s string) string {
	i := strings.Index(s, "\x1b[38;")
	if i < 0 {
		i = strings.Index(s, "\x1b[48;")
	}
	if i < 0 {
		return ""
	}
	end := i + 20
	if end > len(s) {
		end = len(s)
	}
	return s[i:end]
}

func TestReducedMotionStaticSpinner(t *testing.T) {
	t.Setenv("MOW_NO_ANIM", "1")
	m := newModel(testEngine(t), false, false)
	// Static glyph, not an animated spinner frame.
	if got := strings.TrimSpace(xansi.Strip(m.spinnerView())); got != "◇" {
		t.Fatalf("reduced-motion spinner=%q want ◇", got)
	}
	// advanceSpinnerFrame is a no-op under reduced motion (does not panic / change).
	m.advanceSpinnerFrame()
}
