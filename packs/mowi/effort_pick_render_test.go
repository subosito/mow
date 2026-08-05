package mowi

import (
	"strings"
	"testing"

	xansi "github.com/charmbracelet/x/ansi"
)

// newRenderModel builds a ready, sized model so View() takes the default
// branch (mainFrame + overlays) rather than the startup or too-small paths.
func newRenderModel(t *testing.T) *model {
	t.Helper()
	m := newModel(testEngine(t), false, false)
	m.ready = true
	m.width, m.height = 100, 40
	m.layout()
	return m
}

// TestEffortPickerRendersInView is the regression guard for the bug where the
// picker state was set but the overlay was drawn in the !m.ready branch (whose
// content is overwritten on the next line), so /effort never appeared.
func TestEffortPickerRendersInView(t *testing.T) {
	m := newRenderModel(t)

	// Card must not leak into the frame before the picker opens.
	if before := xansi.Strip(m.View().Content); strings.Contains(before, "enter select") {
		t.Fatal("picker chrome visible before /effort was invoked")
	}

	m.openEffortPicker()
	if m.effortPick == nil {
		t.Fatal("openEffortPicker did not set effortPick")
	}

	got := xansi.Strip(m.View().Content)
	if !strings.Contains(got, "effort") {
		t.Fatalf("rendered view is missing the effort title:\n%s", got)
	}
	// The footer hint only exists on the picker card, so it proves the overlay
	// actually reached the composed view instead of being discarded.
	if !strings.Contains(got, "enter select") {
		t.Fatalf("effort picker card was not composed into the view:\n%s", got)
	}
}

// TestEffortPickerRendersLevels asserts the selectable levels are painted, so
// an empty or mis-built card cannot pass the title check alone.
func TestEffortPickerRendersLevels(t *testing.T) {
	m := newRenderModel(t)
	m.openEffortPicker()

	got := xansi.Strip(m.View().Content)
	for _, want := range m.effortPick.items {
		if !strings.Contains(got, want) {
			t.Fatalf("level %q missing from rendered picker:\n%s", want, got)
		}
	}
	// Selection marker for the highlighted row.
	if !strings.Contains(got, "▸") {
		t.Fatalf("no selection marker in rendered picker:\n%s", got)
	}
}

// TestEffortPickerClosedRemovesOverlay proves esc/enter actually tears the
// overlay back down instead of leaving it pinned over the transcript.
func TestEffortPickerClosedRemovesOverlay(t *testing.T) {
	m := newRenderModel(t)
	m.openEffortPicker()
	if got := xansi.Strip(m.View().Content); !strings.Contains(got, "enter select") {
		t.Fatal("picker did not render while open")
	}

	m.closeEffortPicker()
	if m.effortPick != nil {
		t.Fatal("closeEffortPicker left state behind")
	}
	if got := xansi.Strip(m.View().Content); strings.Contains(got, "enter select") {
		t.Fatalf("picker chrome still rendered after close:\n%s", got)
	}
}

// TestEffortSlashRendersPicker drives the real slash-command entry point, so a
// handler that sets state without the render path wired still fails here.
func TestEffortSlashRendersPicker(t *testing.T) {
	m := newRenderModel(t)

	m.handleSlash("/effort")
	if m.effortPick == nil {
		t.Fatal("/effort did not open the picker")
	}
	if got := xansi.Strip(m.View().Content); !strings.Contains(got, "enter select") {
		t.Fatalf("/effort picker not visible in view:\n%s", got)
	}

	// Bare /effort toggles closed again.
	m.handleSlash("/effort")
	if m.effortPick != nil {
		t.Fatal("second /effort did not close the picker")
	}
}
