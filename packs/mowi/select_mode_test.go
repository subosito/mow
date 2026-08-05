package mowi

import "testing"

// Select mode hands the mouse back to the terminal at runtime. Mouse tracking
// is what makes drag-to-select impossible; before this toggle the only way out
// was MOW_MOUSE=0 at startup, which meant losing the session to copy a line.
func TestSelectModeReleasesMouse(t *testing.T) {
	t.Setenv("MOW_MOUSE", "")
	m := freshModel(t)
	if !m.mouseOn() {
		t.Fatal("mouse tracking should default on")
	}
	m.mouseOff = true
	if m.mouseOn() {
		t.Fatal("select mode must release the mouse")
	}
	m.mouseOff = false
	if !m.mouseOn() {
		t.Fatal("leaving select mode must restore tracking")
	}
}

// MOW_MOUSE=0 still wins: a user who opted out at startup must not have
// tracking switched on by toggling select mode off.
func TestSelectModeRespectsEnvOptOut(t *testing.T) {
	t.Setenv("MOW_MOUSE", "0")
	m := freshModel(t)
	if m.mouseOn() {
		t.Fatal("MOW_MOUSE=0 must keep tracking off")
	}
	m.mouseOff = false
	if m.mouseOn() {
		t.Fatal("env opt-out must survive the runtime toggle")
	}
}

// peerModeLabel feeds the status line shown when the toggle fires with no peer
// streaming, so it must describe both states.
func TestPeerModeLabel(t *testing.T) {
	if got := peerModeLabel(true); got != "live text" {
		t.Errorf("expanded label = %q", got)
	}
	if got := peerModeLabel(false); got == "" || got == "live text" {
		t.Errorf("collapsed label = %q", got)
	}
}
