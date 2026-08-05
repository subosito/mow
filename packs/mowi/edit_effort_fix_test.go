package mowi

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// runUserTurn pushes a prompt through the engine so Rewind has history.
func runUserTurn(t *testing.T, m *model, text string) {
	t.Helper()
	if _, err := m.eng.Prompt(context.Background(), text); err != nil {
		t.Fatalf("prompt: %v", err)
	}
}

// Bug 1: arrow-up recall enters edit mode with a visual indicator and a
// status hint; Esc cancels back to a blank normal prompt.
func TestEditModeEscCancel(t *testing.T) {
	m := newModel(testEngine(t), false, false)
	m.width, m.height = 80, 24
	m.layout()
	runUserTurn(t, m, "hello world")

	m.editLast()
	if !m.editingPrompt {
		t.Fatal("editLast should enter edit mode")
	}
	if got := m.ta.Value(); got != "hello world" {
		t.Fatalf("recalled draft = %q", got)
	}
	if !strings.Contains(m.ta.Prompt, "edit") {
		t.Fatalf("edit-mode prompt should be labeled, got %q", m.ta.Prompt)
	}
	last := m.entries[len(m.entries)-1].text
	if !strings.Contains(last, "esc cancels") {
		t.Fatalf("status should explain esc cancel, got %q", last)
	}

	// Esc cancels: draft cleared, edit flag off, prompt restored.
	um, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
	mm := um.(*model)
	if mm.editingPrompt {
		t.Fatal("esc should leave edit mode")
	}
	if got := mm.ta.Value(); got != "" {
		t.Fatalf("esc should clear the draft, got %q", got)
	}
	if strings.Contains(mm.ta.Prompt, "edit") {
		t.Fatalf("prompt should be back to normal, got %q", mm.ta.Prompt)
	}
}

// Bug 1: sending a recalled prompt clears edit mode.
func TestEditModeSendClears(t *testing.T) {
	m := newModel(testEngine(t), false, false)
	m.width, m.height = 80, 24
	m.layout()
	runUserTurn(t, m, "hello again")

	m.editLast()
	if !m.editingPrompt {
		t.Fatal("expected edit mode")
	}
	um, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	mm := um.(*model)
	if mm.editingPrompt {
		t.Fatal("send should clear edit mode")
	}
	if !mm.busy {
		t.Fatal("enter should have submitted the edited prompt")
	}
}

// Bug 1: arrow up on a non-empty prompt must NOT recall (cursor moves only).
func TestArrowUpNonEmptyNoRecall(t *testing.T) {
	m := newModel(testEngine(t), false, false)
	m.width, m.height = 80, 24
	m.layout()
	m.ready = true
	runUserTurn(t, m, "prior message")
	m.ta.SetValue("draft")
	m.syncInputHeight()

	um, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	mm := um.(*model)
	if mm.editingPrompt {
		t.Fatal("arrow up on non-empty input must not enter edit mode")
	}
	if got := mm.ta.Value(); got != "draft" {
		t.Fatalf("draft clobbered: %q", got)
	}
	if mm.pendingRecall {
		t.Fatal("pendingRecall should not arm on non-empty input")
	}
}

// Bug 2: bare /effort opens the picker; /effort again closes it;
// arrow keys move inside; esc closes without applying.
func TestEffortSlashOpensPicker(t *testing.T) {
	m := newModel(testEngine(t), false, false)
	m.width, m.height = 80, 24
	m.layout()

	m.handleSlash("/effort")
	if m.effortPick == nil {
		t.Fatal("bare /effort should open the picker")
	}
	if len(m.effortPick.items) == 0 {
		t.Fatal("picker should list effort levels")
	}

	um, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	mm := um.(*model)
	if mm.effortPick == nil || mm.effortPick.idx == 0 {
		t.Fatal("arrow down should move picker selection")
	}

	um, _ = mm.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
	mm = um.(*model)
	if mm.effortPick != nil {
		t.Fatal("esc should close the effort picker")
	}

	// Toggle: /effort while open closes.
	m.handleSlash("/effort")
	if m.effortPick == nil {
		t.Fatal("/effort should reopen the picker")
	}
	m.handleSlash("/effort")
	if m.effortPick != nil {
		t.Fatal("/effort while open should close the picker")
	}
}

// Bug 2: enter in the picker applies the highlighted level.
func TestEffortPickerEnterApplies(t *testing.T) {
	m := newModel(testEngine(t), false, false)
	m.width, m.height = 80, 24
	m.layout()

	m.handleSlash("/effort")
	if m.effortPick == nil {
		t.Fatal("picker should be open")
	}
	// Move at least one step so we know what was picked.
	um, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	mm := um.(*model)
	want := mm.effortPick.items[mm.effortPick.idx]

	um, _ = mm.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	mm = um.(*model)
	if mm.effortPick != nil {
		t.Fatal("enter should close the picker")
	}
	if got := mm.eng.Effort(); got != want {
		t.Fatalf("effort = %q, want %q", got, want)
	}
}

// Bug 2: /effort <level> applies directly (no picker); unknown level errors.
func TestEffortSlashWithArg(t *testing.T) {
	m := newModel(testEngine(t), false, false)
	m.width, m.height = 80, 24
	m.layout()

	m.handleSlash("/effort high")
	if m.effortPick != nil {
		t.Fatal("/effort high should not open the picker")
	}
	if got := m.eng.Effort(); got != "high" {
		t.Fatalf("effort = %q, want high", got)
	}

	// cmdEffort reports via an async effortMsg — drive it through Update.
	cmd := m.handleSlash("/effort bogus")
	if cmd == nil {
		t.Fatal("expected a cmd from /effort bogus")
	}
	if _, ok := cmd().(effortMsg); !ok {
		t.Fatal("expected effortMsg from /effort bogus")
	}
	if um, _ := m.Update(cmd()); um != nil {
		m = um.(*model)
	}
	found := false
	for _, e := range m.entries {
		if e.kind == kindError && strings.Contains(e.text, "unknown effort") {
			found = true
		}
	}
	if !found {
		t.Fatal("expected unknown-effort error entry")
	}
}

// Bug 2: keys don't leak into the textarea while the effort picker is open.
func TestEffortPickerOwnsTyping(t *testing.T) {
	m := newModel(testEngine(t), false, false)
	m.width, m.height = 80, 24
	m.layout()
	m.ready = true

	m.handleSlash("/effort")
	if m.effortPick == nil {
		t.Fatal("picker should be open")
	}
	um, _ := m.Update(tea.KeyPressMsg{Code: 'x', Text: "x"})
	mm := um.(*model)
	if got := mm.ta.Value(); got != "" {
		t.Fatalf("typing should not reach the prompt while picker open, got %q", got)
	}
}
