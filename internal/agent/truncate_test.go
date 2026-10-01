package agent

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/subosito/mow/internal/llm"
)

// readFooter is read's paging footer, built by concatenation so this file is
// not itself mistaken for a paged read.
const readFooter = "\n…(showing " + "lines 1-2000; continue with offset=2001)"

// The history cap must keep the tail: bash output ends with the failing
// assertion, read ends with its "continue with offset" footer. A head-only cut
// drops exactly those, and the model re-runs the call to get them back.
func TestTruncateToolResultKeepsHeadAndTail(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, body, wantTail string
		framed               bool
	}{
		{
			name:     "bash failure at the end",
			body:     "START\n" + strings.Repeat("noise line\n", 12_000) + "FAIL: TestAnswer\n",
			wantTail: "FAIL: TestAnswer",
			framed:   true,
		},
		{
			name:     "read paging footer",
			body:     strings.Repeat("\tif err != nil { return err }\n", 5_000) + readFooter,
			wantTail: "continue with offset=2001",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := tc.body
			if tc.framed {
				in = WrapUntrusted("n0nce", "bash", in)
			}
			out := TruncateToolResult(in, DefaultMaxToolResultChars)
			if len(out) > DefaultMaxToolResultChars {
				t.Fatalf("len=%d over cap %d", len(out), DefaultMaxToolResultChars)
			}
			if !strings.Contains(out, tc.wantTail) {
				t.Fatalf("tail dropped; want %q, got …%q", tc.wantTail, out[len(out)-120:])
			}
			if !strings.HasPrefix(out, in[:5]) {
				t.Fatal("head dropped")
			}
			if !strings.Contains(out, "elided from the middle") {
				t.Fatal("want an elision notice that says what was lost")
			}
			if tc.framed && !strings.HasSuffix(out, untrustedClose) {
				t.Fatal("untrusted frame must stay closed")
			}
			if !utf8.ValidString(out) {
				t.Fatal("cut split a rune")
			}
		})
	}
}

func TestTruncateToolResultMultibyteBoundaries(t *testing.T) {
	t.Parallel()
	s := strings.Repeat("héllo wörld — ✓ ", 3_000)
	for _, lim := range []int{150, 999, 1_001, 5_003} {
		out := TruncateToolResult(s, lim)
		if !utf8.ValidString(out) {
			t.Fatalf("lim=%d: invalid UTF-8", lim)
		}
		if len(out) > lim+len(truncatedMarker) {
			t.Fatalf("lim=%d: len=%d", lim, len(out))
		}
	}
}

// The per-call trim runs before every model request. It must not rewrite a
// message the provider has already seen, or the prompt cache is invalidated
// from that index on every turn (billed as a cache write).
func TestCapToolResultsKeepsSentPrefixStable(t *testing.T) {
	t.Parallel()
	big := strings.Repeat("x", 20_000)
	msgs := []llm.Message{{Role: "system", Content: "sys"}, {Role: "user", Content: "task"}}
	var prev []llm.Message
	for turn := range 30 {
		msgs = append(msgs,
			llm.Message{Role: "assistant", Content: "call"},
			llm.Message{Role: "tool", Content: big})
		sent := capToolResults(msgs, DefaultMaxToolResultChars)
		for i := range prev {
			if prev[i].Content != sent[i].Content {
				t.Fatalf("turn %d rewrote already-sent message %d (%d -> %d chars)",
					turn, i, len(prev[i].Content), len(sent[i].Content))
			}
		}
		prev = sent
	}
}

// Compaction still reclaims space from old tool output, counted in tool
// results from the end so the boundary does not depend on list length.
func TestTrimAllToolResultsShrinksOnlyOldResults(t *testing.T) {
	t.Parallel()
	var msgs []llm.Message
	for range olderAfterToolMsgs + 4 {
		msgs = append(msgs,
			llm.Message{Role: "assistant", Content: "call"},
			llm.Message{Role: "tool", Content: strings.Repeat("y", 5_000)})
	}
	out := trimAllToolResults(msgs, 4_000, 1_000)
	var sizes []int
	for _, m := range out {
		if m.Role == "tool" {
			sizes = append(sizes, len(m.Content))
		}
	}
	for i, n := range sizes {
		recent := i >= len(sizes)-olderAfterToolMsgs
		if recent && n > 4_000 || !recent && n > 1_000 {
			t.Fatalf("result %d (recent=%v) len=%d", i, recent, n)
		}
	}
}
