package mowi

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"testing"
)

// TestDiffSmokePaintForShellUse paints a fixed unified diff to the TTY and
// hangs until SIGTERM. Used by scripts/smoke-diff-cells.sh so shell-use can
// assert per-cell geometry and band colours without a model endpoint.
//
// Not part of the normal suite: set MOWI_DIFF_SMOKE=1 to enable.
func TestDiffSmokePaintForShellUse(t *testing.T) {
	if os.Getenv("MOWI_DIFF_SMOKE") != "1" {
		t.Skip("set MOWI_DIFF_SMOKE=1 for shell-use paint fixture")
	}
	// Force truecolor; clear NO_COLOR so the band actually paints.
	t.Setenv("MOW_FORCE_COLOR", "1")
	t.Setenv("NO_COLOR", "")

	// Pin adaptive default dark so expected band hex is stable across hosts
	// (DefaultThemeName is catppuccin-mocha and would drift with chroma).
	th := newThemeFrom(ThemeConfig{Name: "default"}, true)
	src := "" +
		"@@ -3,4 +3,4 @@\n" +
		" func New() *Client {\n" +
		"-	timeout := 30\n" +
		"+	timeout := 60\n" +
		" 	return newClient(timeout, false)\n"
	out := renderPrettyDiffPath(th, src, "cfg.go", 80)

	// Clear screen + home, then the diff, then a ready marker for wait.
	fmt.Fprint(os.Stdout, "\x1b[2J\x1b[H")
	fmt.Fprintln(os.Stdout, out)
	fmt.Fprintln(os.Stdout)
	fmt.Fprintln(os.Stdout, "SMOKE_DIFF_READY")
	_ = os.Stdout.Sync()

	// Stay painted until the smoke script kills the session.
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGTERM, os.Interrupt)
	<-ch
}
