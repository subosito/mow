package mowi

import (
	"strings"
	"testing"
	"time"

	xansi "github.com/charmbracelet/x/ansi"
)

// The user block paints its timestamp inline on the first row. That row must
// still fit the viewport: a too-wide first line was clamped by userBlock, which
// silently dropped the end of the typed prompt instead of wrapping it.
func TestUserEntryFirstRowFitsViewportWithStamp(t *testing.T) {
	m := freshModel(t)
	m.width, m.height = 60, 24
	m.layout()

	at := time.Date(2026, 8, 5, 15, 4, 0, 0, time.Local)
	text := strings.Repeat("prompt word ", 12)
	out := xansi.Strip(m.renderEntry(entry{kind: kindUser, text: text, at: at}, m.width))

	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) < 2 {
		t.Fatalf("long prompt should wrap to multiple rows:\n%s", out)
	}
	for _, line := range lines {
		if w := xansi.StringWidth(line); w > m.width {
			t.Fatalf("user row overflows viewport (%d > %d): %q", w, m.width, line)
		}
	}
	if !strings.Contains(lines[0], "15:04") {
		t.Fatalf("first row lost its timestamp: %q", lines[0])
	}
	// Every word survives: truncation used to eat the tail of the first row.
	got := strings.Join(lines, " ")
	if want := strings.Count(strings.TrimSpace(text), "prompt"); strings.Count(got, "prompt") != want {
		t.Fatalf("prompt text truncated: kept %d of %d words\n%s",
			strings.Count(got, "prompt"), want, out)
	}
}
