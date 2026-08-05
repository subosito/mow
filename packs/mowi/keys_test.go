package mowi

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

// freshModel returns a model sized + ready for key-binding tests.
func freshModel(t *testing.T) *model {
	t.Helper()
	m := newModel(testEngine(t), false, false)
	m.width, m.height = 80, 24
	m.layout()
	m.ready = true
	m.showWelcome = false
	return m
}

// tallModel returns a ready model with enough transcript lines to scroll.
func tallModel(t *testing.T) *model {
	t.Helper()
	m := freshModel(t)
	for i := 0; i < 40; i++ {
		m.add(kindStatus, strings.Repeat("line ", 5)+string(rune('a'+i%26)))
	}
	m.refreshVP()
	m.vp.GotoBottom()
	return m
}

// ---------- Defaults & config resolution ----------

func TestDefaultKeys(t *testing.T) {
	d := DefaultKeys()
	cases := map[string]string{
		"send":        d.Send,
		"newline":     d.Newline,
		"cancel":      d.Cancel,
		"quit":        d.Quit,
		"clear":       d.Clear,
		"help":        d.Help,
		"perm_cycle":  d.PermCycle,
		"scroll_up":   d.ScrollUp,
		"scroll_down": d.ScrollDown,
		"focus":       d.Focus,
	}
	expected := map[string]string{
		"send":        "enter",
		"newline":     "ctrl+j",
		"cancel":      "esc",
		"quit":        "ctrl+c",
		"clear":       "ctrl+l",
		"help":        "ctrl+/",
		"perm_cycle":  "shift+tab",
		"scroll_up":   "ctrl+u",
		"scroll_down": "ctrl+d",
		"focus":       "ctrl+o",
	}
	for name, got := range cases {
		if got != expected[name] {
			t.Errorf("%s = %q, want %q", name, got, expected[name])
		}
	}
}

func TestKeysResolveFillsEmpty(t *testing.T) {
	k := KeysConfig{Send: "ctrl+s"} // only send overridden
	r := k.Resolve()
	if r.Send != "ctrl+s" {
		t.Fatalf("Send=%q", r.Send)
	}
	if r.Cancel != "esc" {
		t.Fatalf("Cancel not defaulted: %q", r.Cancel)
	}
	if r.Help != "ctrl+/" {
		t.Fatalf("Help not defaulted: %q", r.Help)
	}
}

func TestKeysMatchesCommaSeparated(t *testing.T) {
	k := KeysConfig{}
	if k.Matches("pgup,ctrl+u", "ctrl+u") == false {
		t.Fatal("should match ctrl+u in pgup,ctrl+u")
	}
	if k.Matches("pgup,ctrl+u", "pgup") == false {
		t.Fatal("should match pgup in pgup,ctrl+u")
	}
	if k.Matches("pgup,ctrl+u", "ctrl+d") {
		t.Fatal("should not match ctrl+d")
	}
	// Empty / nil cases.
	if k.Matches("", "enter") {
		t.Fatal("empty field should not match")
	}
	if k.Matches("enter", "") {
		t.Fatal("empty key should not match")
	}
}

func TestKeysPrimaryAndAll(t *testing.T) {
	k := KeysConfig{}
	if k.Primary("ctrl+u,pgup") != "ctrl+u" {
		t.Fatalf("Primary=%q", k.Primary("ctrl+u,pgup"))
	}
	all := k.All("enter,ctrl+m")
	if len(all) != 2 || all[0] != "enter" || all[1] != "ctrl+m" {
		t.Fatalf("All=%v", all)
	}
	if k.All("") != nil {
		t.Fatal("empty All should return nil")
	}
	if k.Primary("") != "" {
		t.Fatal("empty Primary should return empty")
	}
}

// ---------- Send (enter) ----------

func TestSendSubmitsMessage(t *testing.T) {
	m := freshModel(t)
	m.ta.SetValue("hello")
	mod, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	mm := mod.(*model)
	if mm.busy != true {
		t.Fatal("enter should start a busy turn")
	}
	if len(mm.entries) == 0 || mm.entries[0].text != "hello" {
		t.Fatalf("user entry missing: %v", mm.lines())
	}
}

func TestSendEmptyDoesNothing(t *testing.T) {
	m := freshModel(t)
	mod, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	mm := mod.(*model)
	if mm.busy {
		t.Fatal("empty send should not start a turn")
	}
}

func TestSendWhileBusyQueues(t *testing.T) {
	m := freshModel(t)
	m.busy = true
	m.ta.SetValue("followup")
	mod, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	mm := mod.(*model)
	if !mm.busy {
		t.Fatal("should still be busy")
	}
	if len(mm.queued) != 1 || mm.queued[0] != "followup" {
		t.Fatalf("expected queued followup, got %v", mm.queued)
	}
	if mm.ta.Value() != "" {
		t.Fatalf("textarea should be cleared after queue, got %q", mm.ta.Value())
	}
}

func TestSendSlashCommandNotQueued(t *testing.T) {
	m := freshModel(t)
	m.busy = true
	m.ta.SetValue("/clear")
	mod, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	mm := mod.(*model)
	// /clear acts immediately, not queued.
	if len(mm.queued) != 0 {
		t.Fatalf("slash command should not be queued, got %v", mm.queued)
	}
}

// ---------- Newline (ctrl+j) ----------

func TestNewlineInsertsLine(t *testing.T) {
	m := freshModel(t)
	m.ta.SetValue("line1")
	mod, _ := m.Update(tea.KeyPressMsg{Mod: tea.ModCtrl, Code: 'j'})
	mm := mod.(*model)
	if !strings.Contains(mm.ta.Value(), "\n") {
		t.Fatalf("expected newline in textarea, got %q", mm.ta.Value())
	}
}

// ---------- Cancel (esc) ----------

func TestCancelDismissesWelcome(t *testing.T) {
	m := freshModel(t)
	m.showWelcome = true
	mod, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
	mm := mod.(*model)
	if mm.showWelcome {
		t.Fatal("esc should dismiss welcome")
	}
}

func TestCancelBusyCancelsTurn(t *testing.T) {
	m := freshModel(t)
	m.busy = true
	// Simulate an active cancel func.
	cancelled := false
	m.cancel = func() { cancelled = true }
	mod, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
	mm := mod.(*model)
	if !cancelled {
		t.Fatal("esc should call cancel")
	}
	if len(mm.queued) != 0 {
		t.Fatal("esc should drop queue")
	}
}

func TestCancelIdleDoesNothing(t *testing.T) {
	m := freshModel(t)
	m.showWelcome = false
	mod, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
	mm := mod.(*model)
	// Idle esc with no welcome just returns (no quit, no error).
	if mm.quitting {
		t.Fatal("esc should not quit when idle")
	}
}

// ---------- Quit (ctrl+c) ----------

func TestQuitIdleQuits(t *testing.T) {
	m := freshModel(t)
	mod, _ := m.Update(tea.KeyPressMsg{Mod: tea.ModCtrl, Code: 'c'})
	mm := mod.(*model)
	if !mm.quitting {
		t.Fatal("ctrl+c should set quitting when idle")
	}
}

func TestQuitBusyCancelsTurn(t *testing.T) {
	m := freshModel(t)
	m.busy = true
	cancelled := false
	m.cancel = func() { cancelled = true }
	m.queued = append(m.queued, "pending")
	mod, _ := m.Update(tea.KeyPressMsg{Mod: tea.ModCtrl, Code: 'c'})
	mm := mod.(*model)
	if !cancelled {
		t.Fatal("ctrl+c should cancel turn when busy")
	}
	if len(mm.queued) != 0 {
		t.Fatal("ctrl+c should drop queue")
	}
	if mm.quitting {
		t.Fatal("ctrl+c busy should not set quitting")
	}
}

// ---------- Clear (ctrl+l) ----------

func TestClearWipesTranscript(t *testing.T) {
	m := tallModel(t)
	mod, _ := m.Update(tea.KeyPressMsg{Mod: tea.ModCtrl, Code: 'l'})
	mm := mod.(*model)
	if len(mm.entries) != 0 {
		t.Fatalf("ctrl+l should clear transcript, got %d entries", len(mm.entries))
	}
}

func TestClearWhileBusyDoesNothing(t *testing.T) {
	m := tallModel(t)
	m.busy = true
	n := len(m.entries)
	mod, _ := m.Update(tea.KeyPressMsg{Mod: tea.ModCtrl, Code: 'l'})
	mm := mod.(*model)
	if len(mm.entries) != n {
		t.Fatalf("ctrl+l should not clear while busy, had %d now %d", n, len(mm.entries))
	}
}

// ---------- Help (ctrl+/ / ?) ----------

func TestHelpCtrlSlashOpens(t *testing.T) {
	m := freshModel(t)
	mod, _ := m.Update(tea.KeyPressMsg{Mod: tea.ModCtrl, Code: '/'})
	mm := mod.(*model)
	if !mm.showHelp {
		t.Fatal("ctrl+/ should open help")
	}
}

func TestHelpQuestionMarkEmptyInput(t *testing.T) {
	m := freshModel(t)
	mod, _ := m.Update(tea.KeyPressMsg{Code: '?', Text: "?"})
	mm := mod.(*model)
	if !mm.showHelp {
		t.Fatal("? should open help when input empty")
	}
}

func TestHelpQuestionMarkNonEmptyInputTypes(t *testing.T) {
	m := freshModel(t)
	m.ta.SetValue("text")
	mod, _ := m.Update(tea.KeyPressMsg{Code: '?', Text: "?"})
	mm := mod.(*model)
	if mm.showHelp {
		t.Fatal("? should not open help when input non-empty")
	}
	if !strings.Contains(mm.ta.Value(), "?") {
		t.Fatalf("? should be typed, got %q", mm.ta.Value())
	}
}

func TestHelpWhileBusyDoesNotOpen(t *testing.T) {
	m := freshModel(t)
	m.busy = true
	mod, _ := m.Update(tea.KeyPressMsg{Mod: tea.ModCtrl, Code: '/'})
	mm := mod.(*model)
	if mm.showHelp {
		t.Fatal("ctrl+/ should not open help while busy")
	}
}

func TestHelpCardContainsConfiguredKeys(t *testing.T) {
	m := freshModel(t)
	card := m.helpCard()
	// Should reference key names from the resolved config.
	for _, want := range []string{"send", "perm", "clear", "quit", "help", "focus"} {
		if !strings.Contains(card, want) {
			t.Errorf("helpCard missing %q", want)
		}
	}
}

func TestHelpDismissedByKeys(t *testing.T) {
	// Help is dismissible by cancel, quit, send, ?, q, /
	m := freshModel(t)
	m.showHelp = true
	mod, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
	if mod.(*model).showHelp {
		t.Fatal("esc should close help")
	}

	m.showHelp = true
	mod, _ = m.Update(tea.KeyPressMsg{Mod: tea.ModCtrl, Code: 'c'})
	if mod.(*model).showHelp {
		t.Fatal("ctrl+c should close help")
	}

	m.showHelp = true
	mod, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if mod.(*model).showHelp {
		t.Fatal("enter should close help")
	}

	m.showHelp = true
	mod, _ = m.Update(tea.KeyPressMsg{Code: 'q', Text: "q"})
	if mod.(*model).showHelp {
		t.Fatal("q should close help")
	}

	m.showHelp = true
	mod, _ = m.Update(tea.KeyPressMsg{Code: '?'})
	if mod.(*model).showHelp {
		t.Fatal("? should close help")
	}

	m.showHelp = true
	mod, _ = m.Update(tea.KeyPressMsg{Code: '/'})
	if mod.(*model).showHelp {
		t.Fatal("/ should close help")
	}
}

// ---------- PermCycle (shift+tab) ----------

func TestPermCycleToggles(t *testing.T) {
	m := freshModel(t)
	if m.perm() != PermAuto {
		t.Fatal("expected PermAuto")
	}
	mod, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift})
	mm := mod.(*model)
	if mm.perm() != PermAsk {
		t.Fatalf("shift+tab -> ask, got %v", mm.perm())
	}
	mod, _ = mm.Update(tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift})
	mm = mod.(*model)
	if mm.perm() != PermAuto {
		t.Fatalf("shift+tab -> auto, got %v", mm.perm())
	}
}

func TestPermCycleAddsStatus(t *testing.T) {
	m := freshModel(t)
	mod, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift})
	mm := mod.(*model)
	last := mm.entries[len(mm.entries)-1].text
	if !strings.Contains(last, "perm") || !strings.Contains(last, "ask") {
		t.Fatalf("expected perm status, got %q", last)
	}
}

// ---------- Scroll (ctrl+u / ctrl+d) ----------

func TestScrollUpMovesViewport(t *testing.T) {
	m := tallModel(t)
	yBefore := m.vp.YOffset()
	mod, _ := m.Update(tea.KeyPressMsg{Mod: tea.ModCtrl, Code: 'u'})
	mm := mod.(*model)
	if mm.vp.YOffset() >= yBefore {
		t.Fatalf("ctrl+u should scroll up: before=%d after=%d", yBefore, mm.vp.YOffset())
	}
	if mm.followBottom {
		t.Fatal("ctrl+u should clear followBottom")
	}
}

func TestScrollDownMovesViewport(t *testing.T) {
	m := tallModel(t)
	// Move to top first.
	m.vp.GotoTop()
	yBefore := m.vp.YOffset()
	mod, _ := m.Update(tea.KeyPressMsg{Mod: tea.ModCtrl, Code: 'd'})
	mm := mod.(*model)
	if mm.vp.YOffset() <= yBefore {
		t.Fatalf("ctrl+d should scroll down: before=%d after=%d", yBefore, mm.vp.YOffset())
	}
}

func TestScrollDoesNotAffectInput(t *testing.T) {
	m := tallModel(t)
	m.ta.SetValue("draft")
	mod, _ := m.Update(tea.KeyPressMsg{Mod: tea.ModCtrl, Code: 'u'})
	mm := mod.(*model)
	if mm.ta.Value() != "draft" {
		t.Fatalf("scroll should not change input, got %q", mm.ta.Value())
	}
}

// ---------- Focus (ctrl+o) ----------

func TestFocusToggle(t *testing.T) {
	m := freshModel(t)
	if m.focus != focusEditor {
		t.Fatal("default focus should be editor")
	}
	mod, _ := m.Update(tea.KeyPressMsg{Mod: tea.ModCtrl, Code: 'o'})
	mm := mod.(*model)
	if mm.focus != focusTranscript {
		t.Fatal("ctrl+o should toggle to transcript")
	}
	mod, _ = mm.Update(tea.KeyPressMsg{Mod: tea.ModCtrl, Code: 'o'})
	mm = mod.(*model)
	if mm.focus != focusEditor {
		t.Fatal("ctrl+o should toggle back to editor")
	}
}

func TestTranscriptFocusDropsTyping(t *testing.T) {
	m := freshModel(t)
	m.focus = focusTranscript
	mod, _ := m.Update(tea.KeyPressMsg{Code: 'x', Text: "x"})
	mm := mod.(*model)
	if strings.Contains(mm.ta.Value(), "x") {
		t.Fatalf("transcript focus should drop typing, got %q", mm.ta.Value())
	}
}

func TestTranscriptFocusAllowsScroll(t *testing.T) {
	m := tallModel(t)
	m.focus = focusTranscript
	yBefore := m.vp.YOffset()
	mod, _ := m.Update(tea.KeyPressMsg{Mod: tea.ModCtrl, Code: 'u'})
	mm := mod.(*model)
	if mm.vp.YOffset() >= yBefore {
		t.Fatalf("scroll should work in transcript focus")
	}
}

func TestFocusIndicatorInHeader(t *testing.T) {
	m := freshModel(t)
	m.focus = focusTranscript
	hdr := m.renderHeader()
	if !strings.Contains(hdr, "transcript") {
		t.Fatal("header should show focus:transcript indicator")
	}
	m.focus = focusEditor
	hdr = m.renderHeader()
	if strings.Contains(hdr, "transcript") {
		t.Fatal("header should not show transcript indicator in editor focus")
	}
}

// ---------- Permission prompt keys ----------

func TestPermKeyAllow(t *testing.T) {
	m := freshModel(t)
	resp := make(chan error, 1)
	m.testArmPerm("bash", "$ ls", resp)
	mod, _ := m.Update(tea.KeyPressMsg{Code: 'y', Text: "y"})
	mm := mod.(*model)
	if mm.permWait != nil {
		t.Fatal("y should clear permWait")
	}
	if err := <-resp; err != nil {
		t.Fatalf("y should allow, got %v", err)
	}
}

func TestPermKeyDeny(t *testing.T) {
	m := freshModel(t)
	resp := make(chan error, 1)
	m.testArmPerm("bash", "$ rm -rf", resp)
	mod, _ := m.Update(tea.KeyPressMsg{Code: 'n', Text: "n"})
	mm := mod.(*model)
	if mm.permWait != nil {
		t.Fatal("n should clear permWait")
	}
	if err := <-resp; err == nil {
		t.Fatal("n should deny")
	}
}

func TestPermKeyAlways(t *testing.T) {
	m := freshModel(t)
	resp := make(chan error, 1)
	m.testArmPerm("bash", "$ ls", resp)
	mod, _ := m.Update(tea.KeyPressMsg{Code: 'a', Text: "a"})
	mm := mod.(*model)
	if mm.permWait != nil {
		t.Fatal("a should clear permWait")
	}
	if err := <-resp; err != nil {
		t.Fatalf("a should allow, got %v", err)
	}
	if mm.perm() != PermAuto {
		t.Fatalf("a should set perm to auto, got %v", mm.perm())
	}
	if !mm.autoPower.Load() {
		t.Fatal("a should set autoPower")
	}
}

func TestPermKeyCancel(t *testing.T) {
	m := freshModel(t)
	cancelled := false
	m.cancel = func() { cancelled = true }
	resp := make(chan error, 1)
	m.testArmPerm("bash", "$ ls", resp)
	mod, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
	mm := mod.(*model)
	if mm.permWait != nil {
		t.Fatal("esc should clear permWait")
	}
	if !cancelled {
		t.Fatal("esc should cancel the turn")
	}
	if err := <-resp; err == nil {
		t.Fatal("esc should return error to engine")
	}
}

func TestPermKeyIgnoresUnknown(t *testing.T) {
	m := freshModel(t)
	resp := make(chan error, 1)
	m.testArmPerm("bash", "$ ls", resp)
	// Random letter should not dismiss perm prompt.
	mod, _ := m.Update(tea.KeyPressMsg{Code: 'z', Text: "z"})
	mm := mod.(*model)
	if mm.permWait == nil {
		t.Fatal("z should not dismiss perm prompt")
	}
}

// ---------- Config-driven custom keys ----------

func TestCustomKeyBindings(t *testing.T) {
	m := freshModel(t)
	// Override keys to non-default values.
	m.cfg.Keys = KeysConfig{
		Send:       "ctrl+s",
		Cancel:     "ctrl+g",
		Quit:       "ctrl+q",
		Clear:      "ctrl+k",
		Help:       "f3",
		PermCycle:  "ctrl+p",
		ScrollUp:   "ctrl+u",
		ScrollDown: "ctrl+d",
		Focus:      "ctrl+f",
		Newline:    "ctrl+j",
	}

	// ctrl+s should send (custom send), enter should not.
	m.ta.SetValue("custom send")
	mod, _ := m.Update(tea.KeyPressMsg{Mod: tea.ModCtrl, Code: 's'})
	mm := mod.(*model)
	if !mm.busy {
		t.Fatal("ctrl+s should send (custom)")
	}

	// enter should not send when remapped.
	m2 := freshModel(t)
	m2.cfg.Keys = m.cfg.Keys
	m2.ta.SetValue("no enter send")
	mod, _ = m2.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	mm = mod.(*model)
	if mm.busy {
		t.Fatal("enter should not send when send=ctrl+s")
	}
}

func TestCustomCancelKey(t *testing.T) {
	m := freshModel(t)
	m.cfg.Keys.Cancel = "ctrl+g"
	m.showWelcome = true
	mod, _ := m.Update(tea.KeyPressMsg{Mod: tea.ModCtrl, Code: 'g'})
	mm := mod.(*model)
	if mm.showWelcome {
		t.Fatal("ctrl+g should dismiss welcome (custom cancel)")
	}
}

func TestCustomHelpKey(t *testing.T) {
	m := freshModel(t)
	m.cfg.Keys.Help = "f3"
	mod, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyF1})
	if mod.(*model).showHelp {
		t.Fatal("f1 should not open help when help=f3")
	}
	mod, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyF3})
	if !mod.(*model).showHelp {
		t.Fatal("f3 should open help (custom)")
	}
}

// ---------- Thinking (reserved) ----------

func TestThinkingKeyReserved(t *testing.T) {
	m := freshModel(t)
	// ctrl+t (default thinking) should not crash and not open help.
	mod, _ := m.Update(tea.KeyPressMsg{Mod: tea.ModCtrl, Code: 't'})
	mm := mod.(*model)
	if mm.showHelp {
		t.Fatal("thinking key should not open help")
	}
}

// ---------- Typing while busy ----------

func TestTypingWhileBusy(t *testing.T) {
	m := freshModel(t)
	m.busy = true
	m.startedAt = time.Now()
	m.syncInputChrome()
	mod, _ := m.Update(tea.KeyPressMsg{Code: 'h', Text: "h"})
	mm := mod.(*model)
	if !strings.Contains(mm.ta.Value(), "h") {
		t.Fatalf("should be able to type while busy, got %q", mm.ta.Value())
	}
}
