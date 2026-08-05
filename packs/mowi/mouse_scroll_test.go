package mowi

import (
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

// Mouse wheel must scroll the transcript and never touch the prompt.
//
// Root cause of the classic "wheel recalls the last prompt" bug: with mouse
// tracking OFF (the old default), terminals translate wheel events into
// arrow-key sequences, so wheel-up hit the editor's KeyUp recall path. Mouse
// tracking is now ON by default, so the wheel arrives as a MouseWheelMsg and
// is routed to the viewport only. Two leak shapes must still be guarded:
//   - a terminal that emits an up-arrow escape right after a real mouse event
//     (lastMouseAt grace window), and
//   - MOW_MOUSE=0 opt-out users, where no MouseWheelMsg ever arrives — the
//     arrow-burst guard + recall confirm window handle that path (see
//     TestMouseOffWheelBurstDoesNotRecall).

func TestWheelUpKeepsDraft(t *testing.T) {
	m := freshModel(t)
	m.showWelcome = false
	m.ta.SetValue("draft in progress")
	m.vp.MouseWheelEnabled = true

	mod, _ := m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelUp})
	mm := mod.(*model)
	if mm.ta.Value() != "draft in progress" {
		t.Fatalf("wheel-up changed the prompt to %q", mm.ta.Value())
	}
}

func TestWheelUpDoesNotRecallLastPrompt(t *testing.T) {
	m := freshModel(t)
	m.showWelcome = false
	seedTurn(m, "previous question")
	m.ta.SetValue("")
	m.vp.MouseWheelEnabled = true

	mod, _ := m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelUp})
	mm := mod.(*model)
	if mm.ta.Value() != "" {
		t.Fatalf("wheel-up recalled the last prompt: %q", mm.ta.Value())
	}
}

func TestUpArrowRecallSuppressedRightAfterMouse(t *testing.T) {
	m := freshModel(t)
	m.showWelcome = false
	seedTurn(m, "previous question")
	m.ta.SetValue("")
	// Simulate a wheel event consumed just now (terminal leak window).
	m.lastMouseAt = time.Now()

	mod, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	mm := mod.(*model)
	if mm.ta.Value() != "" {
		t.Fatalf("up-arrow right after a mouse event recalled the prompt: %q", mm.ta.Value())
	}
}

func TestUpArrowStillRecallsAfterGrace(t *testing.T) {
	m := freshModel(t)
	m.showWelcome = false
	seedTurn(m, "recall me")
	m.ta.SetValue("")
	m.lastMouseAt = time.Now().Add(-time.Second) // well past the grace window

	mod, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	if got := mod.(*model).ta.Value(); got != "recall me" {
		t.Fatalf("up-arrow after the grace window recalled %q, want %q", got, "recall me")
	}
}

func TestWheelArmsGraceWindow(t *testing.T) {
	m := freshModel(t)
	m.showWelcome = false
	before := time.Now().Add(-time.Minute)
	m.lastMouseAt = before
	_, _ = m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelUp})
	if !m.lastMouseAt.After(before) {
		t.Fatalf("wheel event did not arm the KeyUp grace window")
	}
}

func TestWheelDoesNotMoveTextareaViewportOrCursor(t *testing.T) {
	m := freshModel(t)
	m.showWelcome = false
	m.ta.SetValue("line one\nline two\nline three\nline four")
	m.ta.CursorStart()
	before := m.ta.Line()
	m.vp.MouseWheelEnabled = true

	mod, _ := m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelDown})
	mm := mod.(*model)
	if got := mm.ta.Line(); got != before {
		t.Fatalf("wheel moved textarea cursor line from %d to %d", before, got)
	}
	if got := mm.ta.Value(); got != "line one\nline two\nline three\nline four" {
		t.Fatalf("wheel changed draft: %q", got)
	}
}

func TestWheelDownKeepsDraftAndDoesNotClear(t *testing.T) {
	m := freshModel(t)
	m.showWelcome = false
	m.ta.SetValue("draft in progress")
	m.vp.MouseWheelEnabled = true

	mod, _ := m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelDown})
	mm := mod.(*model)
	if mm.ta.Value() != "draft in progress" {
		t.Fatalf("wheel-down changed the prompt to %q", mm.ta.Value())
	}
}

// MOW_MOUSE=0: terminals translate wheel events into rapid arrow-key bursts
// (no MouseWheelMsg ever arrives, so the lastMouseAt grace cannot arm). The
// burst must be dropped: no recall fires even after the confirm window.
func TestMouseOffWheelBurstDoesNotRecall(t *testing.T) {
	t.Setenv("MOW_MOUSE", "0")
	m := freshModel(t)
	m.showWelcome = false
	seedTurn(m, "previous question")
	m.ta.SetValue("")

	// Notch 1: up-arrow on an empty prompt → recall is held for confirmation.
	mod, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	mm := mod.(*model)
	if mm.ta.Value() != "" {
		t.Fatalf("up-arrow recalled immediately: %q", mm.ta.Value())
	}
	if !mm.pendingRecall {
		t.Fatal("recall not held for the confirm window")
	}
	// Notch 2 (a few ms later, same spin): cancels the held recall.
	mod, _ = mm.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	mm = mod.(*model)
	if mm.pendingRecall {
		t.Fatal("wheel burst did not cancel the held recall")
	}
	// Confirm window elapses with nothing more: still no recall.
	mm.pendingRecallAt = time.Now().Add(-time.Second)
	_, _ = mm.Update(recallConfirmMsg{})
	if mm.ta.Value() != "" {
		t.Fatalf("wheel burst recalled the prompt: %q", mm.ta.Value())
	}
}

// With mouse off, a SINGLE deliberate up-arrow still recalls after the confirm
// window — recall stays arrow-key-only, as designed.
func TestMouseOffSingleArrowStillRecalls(t *testing.T) {
	t.Setenv("MOW_MOUSE", "0")
	m := freshModel(t)
	m.showWelcome = false
	seedTurn(m, "recall me")
	m.ta.SetValue("")

	mod, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	mm := mod.(*model)
	if !mm.pendingRecall {
		t.Fatal("single up-arrow should hold recall for confirmation")
	}
	mm.pendingRecallAt = time.Now().Add(-time.Second)
	mod, _ = mm.Update(recallConfirmMsg{})
	if got := mod.(*model).ta.Value(); got != "recall me" {
		t.Fatalf("single up-arrow recalled %q, want %q", got, "recall me")
	}
}
