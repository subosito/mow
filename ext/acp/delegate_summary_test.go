package acp

import (
	"strings"
	"testing"
)

func TestFormatDelegateResultPrefersSummary(t *testing.T) {
	reply := "long preamble\n\n## Summary\nFixed the bug in parser.\n"
	out := formatDelegateResult("peer", "end_turn", reply)
	if !strings.Contains(out, "## Peer summary") || !strings.Contains(out, "Fixed the bug") {
		t.Fatalf("got %q", out)
	}
}

func TestFormatDelegateResultTruncates(t *testing.T) {
	body := strings.Repeat("x", defaultDelegateSummaryChars+500)
	out := formatDelegateResult("peer", "end_turn", body)
	if !strings.Contains(out, "summarized for parent context") {
		t.Fatalf("expected truncation marker: %q", out[:min(200, len(out))])
	}
}
