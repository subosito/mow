package mowi

import (
	"os"
	"strconv"
	"strings"
	"sync"

	"github.com/muesli/termenv"
)

var (
	themeOnce sync.Once
	themeDark bool = true // default dark when we cannot tell without probing
)

// pinTerminalTheme resolves light/dark once, before Bubble Tea owns the TTY.
//
// Calling termenv.HasDarkBackground() *during* the TUI is unsafe: it issues
// OSC 11 queries whose replies leak into the input stream as garbage like
// "]11;rgb:1e1e/1e1e/2e2e" and can block the UI for seconds.
//
// Lip Gloss v2 no longer has SetHasDarkBackground; we pin themeDark and build
// fixed-color styles from it (see newThemeFrom).
func pinTerminalTheme() bool {
	themeOnce.Do(func() {
		themeDark = detectDarkBackground()
	})
	return themeDark
}

// darkTheme reports the pinned value (defaults to dark if pinTerminalTheme not called).
func darkTheme() bool {
	return themeDark
}

func detectDarkBackground() bool {
	// Prefer COLORFGBG (no OSC): "fg;bg" ANSI indices, e.g. "15;0".
	if bg := colorFGBGBackground(); bg >= 0 {
		// Light backgrounds are typically 7 (white) or 15 (bright white).
		return bg != 7 && bg != 15
	}
	// One-shot OSC probe *before* alt-screen / Bubble Tea. Response is consumed
	// by termenv here, not mixed into the TUI input loop.
	return termenv.HasDarkBackground()
}

// colorFGBGBackground returns the bg index from COLORFGBG, or -1.
func colorFGBGBackground() int {
	v := os.Getenv("COLORFGBG")
	if v == "" || !strings.Contains(v, ";") {
		return -1
	}
	parts := strings.Split(v, ";")
	n, err := strconv.Atoi(parts[len(parts)-1])
	if err != nil {
		return -1
	}
	return n
}
