package rpc

import (
	"strings"
	"testing"

	"github.com/subosito/mow/cliutil"
)

// A TUI host spawns `mow rpc` and inherits its stderr, so anything printed
// there lands on the terminal outside the host's frame and corrupts the
// display. Tool activity must travel as event notifications on stdout only.
//
// This guards the call site: rpc must not build its Engine with the CLI
// helper, which installs the "→ bash …" stderr progress printer.
func TestRPCDoesNotInstallStderrProgress(t *testing.T) {
	src := readSource(t, "cmd.go")
	if strings.Contains(src, "NewEngineCLI()") {
		t.Error("rpc must not use NewEngineCLI: it prints tool progress to stderr, " +
			"which a TUI host inherits and paints over its own frame")
	}
	if !strings.Contains(src, "ef.Options()") {
		t.Error("rpc should build its Engine from ef.Options() (no stderr progress)")
	}
	// Fail loudly if the CLI helper stops being the thing that adds progress,
	// so this test cannot quietly become vacuous.
	if cliutil.ToolProgressOnEvent(false) == nil {
		t.Fatal("cliutil.ToolProgressOnEvent no longer returns a handler")
	}
}
