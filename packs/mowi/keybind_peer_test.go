package mowi

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestPeerExpandKeyTogglesLive(t *testing.T) {
	m := freshModel(t)
	m.showWelcome = false
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	if !m.peerLiveCollapsed() {
		t.Fatal("want collapsed default")
	}
	m.Update(tea.KeyPressMsg{Code: 'p', Mod: tea.ModCtrl})
	if m.peerLiveCollapsed() {
		t.Fatal("ctrl+p did not expand peers")
	}
	m.Update(tea.KeyPressMsg{Code: 'p', Mod: tea.ModCtrl})
	if !m.peerLiveCollapsed() {
		t.Fatal("ctrl+p did not collapse back")
	}
}

func TestSelectModeKeyReleasesMouse(t *testing.T) {
	t.Setenv("MOW_MOUSE", "")
	m := freshModel(t)
	m.showWelcome = false
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	if !m.mouseOn() {
		t.Fatal("want tracking on")
	}
	m.Update(tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
	if m.mouseOn() {
		t.Fatal("ctrl+s did not release the mouse")
	}
	// Persistent state lives on the header chip, not in scrollback.
	if !strings.Contains(m.renderHeader(), "select") {
		t.Fatal("header has no select-mode chip")
	}
	// The transcript teaches the mechanics exactly once per session.
	var teaches int
	for _, e := range m.entries {
		if strings.Contains(e.text, "select mode") {
			teaches++
		}
	}
	if teaches != 1 {
		t.Fatalf("want one select-mode teach line, got %d", teaches)
	}
	// Toggling back and forth again must not spam the transcript.
	m.Update(tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
	if !m.mouseOn() {
		t.Fatal("ctrl+s did not restore tracking")
	}
	if strings.Contains(m.renderHeader(), "select") {
		t.Fatal("header chip should disappear when tracking is back")
	}
	m.Update(tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
	if m.mouseOn() {
		t.Fatal("second ctrl+s did not release the mouse")
	}
	teaches = 0
	for _, e := range m.entries {
		if strings.Contains(e.text, "select mode") {
			teaches++
		}
	}
	if teaches != 1 {
		t.Fatalf("teach must fire once, got %d lines", teaches)
	}
	if !strings.Contains(m.renderHeader(), "select") {
		t.Fatal("header chip missing after re-entering select mode")
	}
}
