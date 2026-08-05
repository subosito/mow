package llm

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// truncate clamps provider error bodies, which are untrusted external bytes and
// routinely non-ASCII. Cutting mid-rune emitted invalid UTF-8 into an error
// string that then flows to logs, sessions, and the model.
func TestTruncateCutsOnRuneBoundary(t *testing.T) {
	const body = "エラー: 認証に失敗しました"
	for n := 1; n < len(body); n++ {
		got := truncate(body, n)
		if !utf8.ValidString(got) {
			t.Fatalf("truncate(%d) produced invalid UTF-8: %q", n, got)
		}
		if !strings.HasSuffix(got, "…") {
			t.Fatalf("truncate(%d) lost its ellipsis: %q", n, got)
		}
		if len(got) > n+len("…") {
			t.Fatalf("truncate(%d) = %d bytes, over budget: %q", n, len(got), got)
		}
	}
}

func TestTruncateShortInputUnchanged(t *testing.T) {
	if got := truncate("ok", 10); got != "ok" {
		t.Fatalf("got %q, want the input unchanged", got)
	}
	// Exactly at the limit is not truncated.
	if got := truncate("abcde", 5); got != "abcde" {
		t.Fatalf("got %q, want no ellipsis at the boundary", got)
	}
}
