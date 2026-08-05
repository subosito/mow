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
	var found bool
	for _, e := range m.entries {
		if strings.Contains(e.text, "select mode on") {
			found = true
		}
	}
	if !found {
		t.Fatal("no status line telling the user select mode is on")
	}
}
