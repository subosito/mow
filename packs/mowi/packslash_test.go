package mowi

import (
	"context"
	"strings"
	"testing"

	"github.com/subosito/mow/slash"
)

// These tests are about the wiring, not about any one pack: the TUI must run
// whatever is registered and must not name a pack. A test that asserted
// "/review works" would pass just as well with the old hardcoded switch.

func TestPackSlashDispatchesRegistered(t *testing.T) {
	slash.Register(slash.Command{
		Name:    "mowitest",
		Summary: "test command",
		Run: func(context.Context, slash.Request) (slash.Result, error) {
			return slash.Result{Title: "t", Body: "b"}, nil
		},
	})
	if !isPackSlash("/mowitest") {
		t.Error("registered command not dispatchable")
	}
	if isPackSlash("/definitely-not-registered") {
		t.Error("unregistered token reported as dispatchable")
	}
}

func TestBuiltinsShadowPacks(t *testing.T) {
	// A pack must never be able to capture /quit: if it could, a buggy pack
	// would make a session unexitable.
	for _, tok := range []string{"/quit", "/exit", "/clear", "/help", "/model"} {
		if !builtinSlash(tok) {
			t.Errorf("builtinSlash(%q) = false, want true", tok)
		}
	}
	if builtinSlash("/review") {
		t.Error("builtinSlash(/review) = true; review comes from the registry, not the switch")
	}
}

func TestFramePackSlashDoesNotParseBody(t *testing.T) {
	// The frame must pass the pack's body through untouched: a pack that
	// changes its report layout must not be able to break the TUI frame.
	body := "## not a heading we own\n\n- item\n|table|"
	got := framePackSlash("title", body)
	if !strings.Contains(got, body) {
		t.Errorf("body was altered:\n%s", got)
	}
	if !strings.HasPrefix(got, "# title") {
		t.Errorf("missing frame heading:\n%s", got)
	}
}

func TestPackSlashHelpIsSynchronous(t *testing.T) {
	// Help must not enter busy state: there is no engine work to cancel, and
	// a spinner that never resolves would wedge the input.
	slash.Register(slash.Command{
		Name:  "mowihelp",
		Usage: "usage text",
		Run: func(context.Context, slash.Request) (slash.Result, error) {
			t.Error("Run called for a help request")
			return slash.Result{}, nil
		},
	})
	m := &model{}
	cmd, handled := m.handlePackSlash([]string{"/mowihelp", "help"})
	if !handled {
		t.Fatal("help not handled")
	}
	if cmd != nil {
		t.Error("help returned an async command, want synchronous")
	}
	if m.busy {
		t.Error("help set busy state")
	}
}

func TestPackSlashRefusesExclusiveWhileBusy(t *testing.T) {
	// Exclusive commands share the session engine; starting one mid-turn
	// would interleave two conversations on one history.
	slash.Register(slash.Command{
		Name:      "mowibusy",
		Exclusive: true,
		Run: func(context.Context, slash.Request) (slash.Result, error) {
			t.Error("Run called while busy")
			return slash.Result{}, nil
		},
	})
	m := &model{busy: true}
	cmd, handled := m.handlePackSlash([]string{"/mowibusy"})
	if !handled {
		t.Fatal("command not handled")
	}
	if cmd != nil {
		t.Error("started an exclusive command during a turn")
	}
}

func TestPackSlashIgnoresUnknown(t *testing.T) {
	// Unhandled tokens must fall through so the built-in switch sees them,
	// and so a line like "/tmp is full" still reaches the model as a prompt.
	m := &model{}
	cmd, handled := m.handlePackSlash([]string{"/no-such-command-xyz"})
	if handled || cmd != nil {
		t.Error("unknown token was handled")
	}
	if c, h := m.handlePackSlash(nil); h || c != nil {
		t.Error("empty parts were handled")
	}
}
