package mowi

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	xansi "github.com/charmbracelet/x/ansi"
)

func TestClampFrameLinesPreservesANSIAndWidth(t *testing.T) {
	in := lipgloss.NewStyle().Foreground(lipgloss.Color("#ff00ff")).Render(strings.Repeat("x", 20)) + "\nshort"
	got := clampFrameLines(in, 8)
	for _, line := range strings.Split(got, "\n") {
		if w := xansi.StringWidth(line); w > 8 {
			t.Fatalf("line width=%d: %q", w, line)
		}
	}
	if !strings.Contains(xansi.Strip(got), "short") {
		t.Fatalf("clamp dropped later line: %q", got)
	}
}

func TestRenderInputKeepsLongPromptWithinViewport(t *testing.T) {
	m := freshModel(t)
	m.width, m.height = 40, 20
	m.ta.SetValue(strings.Repeat("long prompt word ", 12))
	m.layout()
	out := xansi.Strip(m.renderInput())
	for _, line := range strings.Split(out, "\n") {
		if w := xansi.StringWidth(line); w > m.width {
			t.Fatalf("input line overflows viewport: width=%d line=%q\n%s", w, line, out)
		}
	}
}
