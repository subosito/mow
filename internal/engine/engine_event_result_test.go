package engine

import (
	"strings"
	"testing"
)

func TestEventToolResultKeepsEditDiff(t *testing.T) {
	body := strings.Repeat("x", 5000)
	edit := "edited f.go\n--- f.go\n+++ f.go\n@@ -1 +1 @@\n-" + body + "\n+new\n"
	if got := eventToolResult("edit", edit); got != edit {
		t.Fatalf("edit EventToolEnd must keep the hunk, got %d bytes", len(got))
	}
	got := eventToolResult("read", edit)
	want := 4000 + len("…(truncated)")
	if len(got) != want || !strings.HasSuffix(got, "…(truncated)") {
		t.Fatalf("read EventToolEnd clip: len=%d want=%d suffix=%q", len(got), want, got[max(0, len(got)-20):])
	}
}
