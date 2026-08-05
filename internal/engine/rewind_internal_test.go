package engine

import (
	"testing"

	"github.com/subosito/mow/internal/llm"
)

// Rewind must land on the user's own prompt, skipping host-injected synthetic
// user messages (thrash/explore warnings, mid-turn steer) and tool turns — so
// TUI edit/retry/↑ never load a nudge string as "the last prompt".
func TestRewindSkipsSyntheticUserMessages(t *testing.T) {
	e := &Engine{
		prior: []llm.Message{
			{Role: "user", Content: "real prompt"},
			{Role: "assistant", Content: "let me look", ToolCalls: []llm.ToolCall{{ID: "t1"}}},
			{Role: "tool", Content: "ok", ToolCallID: "t1"},
			{Role: "user", Content: "Note: 3 consecutive explore-only turns…", Synthetic: true},
			{Role: "assistant", Content: "done"},
		},
		transcript: []Message{
			{Role: "user", Content: "real prompt"},
			{Role: "assistant", Content: "let me look"},
			{Role: "assistant", Content: "done"},
		},
	}
	last, ok := e.Rewind()
	if !ok || last != "real prompt" {
		t.Fatalf("rewind=(%q,%v), want real prompt", last, ok)
	}
	if len(e.prior) != 0 {
		t.Fatalf("prior after rewind = %d msgs, want 0 (everything was one turn)", len(e.prior))
	}
}

// A synthetic steer trailing the exchange is also skipped, even when it is the
// very last user message in history.
func TestRewindSkipsTrailingSteer(t *testing.T) {
	e := &Engine{
		prior: []llm.Message{
			{Role: "user", Content: "real prompt"},
			{Role: "assistant", Content: "call", ToolCalls: []llm.ToolCall{{ID: "t1"}}},
			{Role: "tool", Content: "ok", ToolCallID: "t1"},
			{Role: "user", Content: "steer note", Synthetic: true},
		},
		transcript: []Message{
			{Role: "user", Content: "real prompt"},
			{Role: "assistant", Content: "call"},
		},
	}
	last, ok := e.Rewind()
	if !ok || last != "real prompt" {
		t.Fatalf("rewind=(%q,%v), want real prompt", last, ok)
	}
}

// Without any synthetic messages behavior is unchanged: a plain last user turn
// is rewound as before.
func TestRewindStillHandlesPlainUserTurn(t *testing.T) {
	e := &Engine{
		prior: []llm.Message{
			{Role: "user", Content: "first"},
			{Role: "assistant", Content: "a1"},
			{Role: "user", Content: "second"},
			{Role: "assistant", Content: "a2"},
		},
		transcript: []Message{
			{Role: "user", Content: "first"},
			{Role: "assistant", Content: "a1"},
			{Role: "user", Content: "second"},
			{Role: "assistant", Content: "a2"},
		},
	}
	last, ok := e.Rewind()
	if !ok || last != "second" {
		t.Fatalf("rewind=(%q,%v), want second", last, ok)
	}
}
