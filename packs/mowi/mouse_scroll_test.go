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
//   - MOW_MOUSE=0 / select mode, where no MouseWheelMsg ever arrives — the
//     mouse-off arrow path (consumeMouseOffArrow) handles that: scroll on
//     bursts, delayed edit-last only for a deliberate lone Up.

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

// mouseOffModel is a tall transcript with tracking released (select mode or
// MOW_MOUSE=0). Callers choose how tracking is released.
func mouseOffTall(t *testing.T) *model {
	t.Helper()
	m := tallModel(t)
	m.mouseOff = true
	if m.mouseOn() {
		t.Fatal("fixture must have mouse tracking off")
	}
	seedTurn(m, "previous question")
	m.ta.SetValue("")
	m.syncInputHeight()
	// Pin mid-scroll so both up and down arrows can move YOffset.
	m.followBottom = false
	m.vp.GotoBottom()
	total := m.vp.TotalLineCount()
	vis := m.vp.VisibleLineCount()
	if total > vis+4 {
		m.vp.SetYOffset(total - vis - 4)
	}
	return m
}

// ---------- Mouse-off arrow path (select mode / MOW_MOUSE=0) ----------

// Wheel burst: first Up arms pending recall; second cancels and scrolls.
// Confirm timer later must not edit-last. Deterministic: no real sleeps —
// second Update is immediate (always within arrowBurstGap), confirm is a
// synthetic recallConfirmMsg with pendingRecallAt forced into the past.
func TestMouseOffWheelBurstDoesNotRecall(t *testing.T) {
	t.Setenv("MOW_MOUSE", "0")
	m := mouseOffTall(t)
	y0 := m.vp.YOffset()

	// Notch 1: up-arrow on an empty prompt → recall is held for confirmation.
	mod, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	mm := mod.(*model)
	if mm.ta.Value() != "" {
		t.Fatalf("up-arrow recalled immediately: %q", mm.ta.Value())
	}
	if !mm.pendingRecall {
		t.Fatal("recall not held for the confirm window")
	}
	if mm.vp.YOffset() != y0 {
		t.Fatal("first candidate Up must not scroll yet")
	}

	// Notch 2 (same spin): cancels the held recall and scrolls.
	mod, _ = mm.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	mm = mod.(*model)
	if mm.pendingRecall {
		t.Fatal("wheel burst did not cancel the held recall")
	}
	if mm.vp.YOffset() >= y0 {
		t.Fatalf("burst arrow should scroll up: y0=%d y=%d", y0, mm.vp.YOffset())
	}
	if mm.ta.Value() != "" {
		t.Fatalf("burst changed draft: %q", mm.ta.Value())
	}

	// Confirm window elapses with nothing more: still no recall.
	mm.pendingRecallAt = time.Now().Add(-time.Second)
	_, _ = mm.Update(recallConfirmMsg{})
	if mm.ta.Value() != "" {
		t.Fatalf("wheel burst recalled the prompt: %q", mm.ta.Value())
	}
}

// Slow second arrow (outside arrowBurstGap) must still cancel pending recall:
// real wheel notches can space arrows >100ms under load; users never double-tap
// Up to edit.
func TestMouseOffSlowSecondArrowCancelsRecall(t *testing.T) {
	m := mouseOffTall(t)
	y0 := m.vp.YOffset()

	mod, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	mm := mod.(*model)
	if !mm.pendingRecall {
		t.Fatal("want pending recall after first Up")
	}
	// Force the gap past arrowBurstGap while pending is still set.
	mm.lastArrowAt = time.Now().Add(-time.Second)

	mod, _ = mm.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	mm = mod.(*model)
	if mm.pendingRecall {
		t.Fatal("second arrow must cancel pending even outside burst gap")
	}
	if mm.vp.YOffset() >= y0 {
		t.Fatalf("second arrow should scroll: y0=%d y=%d", y0, mm.vp.YOffset())
	}
	mm.pendingRecallAt = time.Now().Add(-time.Second)
	_, _ = mm.Update(recallConfirmMsg{})
	if mm.ta.Value() != "" {
		t.Fatalf("slow wheel notch recalled: %q", mm.ta.Value())
	}
}

// After a confirmed burst, sticky wheel mode keeps later arrows scrolling and
// prevents re-arming edit-last between slow notches of one spin.
func TestMouseOffStickyWheelDoesNotRearmRecall(t *testing.T) {
	m := mouseOffTall(t)

	mod, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	mm := mod.(*model)
	// Second arrow: enter sticky.
	mod, _ = mm.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	mm = mod.(*model)
	if mm.pendingRecall {
		t.Fatal("burst should clear pending")
	}
	if !time.Now().Before(mm.wheelUntil) {
		t.Fatal("burst should arm wheel sticky window")
	}
	// Simulate a pause longer than arrowBurstGap but still inside sticky.
	mm.lastArrowAt = time.Now().Add(-time.Second)
	// Keep sticky alive regardless of wall clock drift in slow CI.
	mm.wheelUntil = time.Now().Add(wheelSticky)

	yBefore := mm.vp.YOffset()
	mod, _ = mm.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	mm = mod.(*model)
	if mm.pendingRecall {
		t.Fatal("sticky wheel must not re-arm pending recall")
	}
	if mm.vp.YOffset() >= yBefore {
		t.Fatalf("sticky arrow should scroll: before=%d after=%d", yBefore, mm.vp.YOffset())
	}
	if mm.ta.Value() != "" {
		t.Fatalf("sticky path recalled: %q", mm.ta.Value())
	}
}

// Deliberate lone Up still recalls after the confirm window — no second arrow.
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
	if mm.ta.Value() != "" {
		t.Fatalf("must not recall before confirm: %q", mm.ta.Value())
	}
	// Deterministic timer: expire the window without sleeping.
	mm.pendingRecallAt = time.Now().Add(-time.Second)
	mod, _ = mm.Update(recallConfirmMsg{})
	if got := mod.(*model).ta.Value(); got != "recall me" {
		t.Fatalf("single up-arrow recalled %q, want %q", got, "recall me")
	}
}

// Select-mode toggle (runtime mouseOff) uses the same path as MOW_MOUSE=0.
func TestSelectModeWheelBurstScrollsNoRecall(t *testing.T) {
	t.Setenv("MOW_MOUSE", "")
	m := mouseOffTall(t) // mouseOff=true, env default on
	y0 := m.vp.YOffset()

	mod, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	mm := mod.(*model)
	if !mm.pendingRecall {
		t.Fatal("select-mode first Up should arm pending")
	}
	mod, _ = mm.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	mm = mod.(*model)
	if mm.pendingRecall {
		t.Fatal("burst must cancel pending")
	}
	if mm.vp.YOffset() >= y0 {
		t.Fatalf("select-mode wheel should scroll: y0=%d y=%d", y0, mm.vp.YOffset())
	}
	mm.pendingRecallAt = time.Now().Add(-time.Second)
	_, _ = mm.Update(recallConfirmMsg{})
	if mm.ta.Value() != "" {
		t.Fatalf("select-mode wheel recalled: %q", mm.ta.Value())
	}
}

// Down-wheel (KeyDown sequences) scrolls and never touches the draft.
func TestMouseOffDownArrowScrolls(t *testing.T) {
	m := mouseOffTall(t)
	m.vp.GotoTop()
	y0 := m.vp.YOffset()
	m.ta.SetValue("draft stays")

	mod, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	mm := mod.(*model)
	if mm.vp.YOffset() <= y0 {
		t.Fatalf("down arrow should scroll down: y0=%d y=%d", y0, mm.vp.YOffset())
	}
	if mm.ta.Value() != "draft stays" {
		t.Fatalf("down arrow changed draft: %q", mm.ta.Value())
	}
	if mm.pendingRecall {
		t.Fatal("down must not arm pending recall")
	}
}

// Busy turn: arrows still scroll; never edit-last.
func TestMouseOffArrowScrollsWhileBusy(t *testing.T) {
	m := mouseOffTall(t)
	m.busy = true
	m.ta.SetValue("")
	y0 := m.vp.YOffset()

	mod, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	mm := mod.(*model)
	if mm.pendingRecall {
		t.Fatal("busy must not arm pending recall")
	}
	if mm.vp.YOffset() >= y0 {
		t.Fatalf("busy wheel-up should scroll: y0=%d y=%d", y0, mm.vp.YOffset())
	}
	if mm.ta.Value() != "" {
		t.Fatalf("busy arrow recalled: %q", mm.ta.Value())
	}
}

// Non-empty draft: Up does not arm edit-last; it scrolls (select mode owns
// arrows for the transcript, not the textarea cursor).
func TestMouseOffArrowNonEmptyScrollsNoRecall(t *testing.T) {
	m := mouseOffTall(t)
	m.ta.SetValue("draft")
	y0 := m.vp.YOffset()

	mod, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	mm := mod.(*model)
	if mm.pendingRecall {
		t.Fatal("non-empty must not arm pending")
	}
	if mm.editingPrompt {
		t.Fatal("must not enter edit mode")
	}
	if mm.ta.Value() != "draft" {
		t.Fatalf("draft clobbered: %q", mm.ta.Value())
	}
	if mm.vp.YOffset() >= y0 {
		t.Fatalf("non-empty Up should scroll: y0=%d y=%d", y0, mm.vp.YOffset())
	}
}

// Typing while pending cancels edit-last without firing it.
func TestMouseOffPendingCanceledByType(t *testing.T) {
	m := freshModel(t)
	m.mouseOff = true
	seedTurn(m, "recall me")
	m.ta.SetValue("")

	mod, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	mm := mod.(*model)
	if !mm.pendingRecall {
		t.Fatal("want pending")
	}
	// A printable key cancels the held recall.
	mod, _ = mm.Update(tea.KeyPressMsg{Code: 'x', Text: "x"})
	mm = mod.(*model)
	if mm.pendingRecall {
		t.Fatal("typing should cancel pending recall")
	}
	mm.pendingRecallAt = time.Now().Add(-time.Second)
	_, _ = mm.Update(recallConfirmMsg{})
	if mm.ta.Value() == "recall me" {
		t.Fatal("canceled pending must not edit-last on timer")
	}
}

// recallConfirmMsg is a no-op when sticky wheel mode is still active even if
// pending was left set (defensive: burst path clears pending, but the timer
// check must not race a late tick).
func TestMouseOffConfirmIgnoredDuringSticky(t *testing.T) {
	m := freshModel(t)
	m.mouseOff = true
	seedTurn(m, "recall me")
	m.ta.SetValue("")
	// Synthesize a stale pending + active sticky (as if a late tick arrived
	// after a burst that somehow left pending set).
	m.pendingRecall = true
	m.pendingRecallAt = time.Now().Add(-time.Second)
	m.wheelUntil = time.Now().Add(time.Second)

	mod, _ := m.Update(recallConfirmMsg{})
	mm := mod.(*model)
	if mm.ta.Value() != "" {
		t.Fatalf("sticky window must block edit-last: %q", mm.ta.Value())
	}
	if mm.pendingRecall {
		t.Fatal("confirm should clear pending even when sticky blocks edit")
	}
}

// Multi-arrow burst of Down keys scrolls without touching a draft.
func TestMouseOffDownBurstScrollsKeepsDraft(t *testing.T) {
	m := mouseOffTall(t)
	m.vp.GotoTop()
	m.ta.SetValue("keep me")
	y0 := m.vp.YOffset()

	for i := 0; i < 3; i++ {
		mod, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
		m = mod.(*model)
	}
	if m.vp.YOffset() <= y0 {
		t.Fatalf("down burst should scroll: y0=%d y=%d", y0, m.vp.YOffset())
	}
	if m.ta.Value() != "keep me" {
		t.Fatalf("down burst changed draft: %q", m.ta.Value())
	}
}
