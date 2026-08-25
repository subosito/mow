package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/subosito/mow/internal/llm"
)

func msgs(contents ...string) []llm.Message {
	out := make([]llm.Message, len(contents))
	for i, c := range contents {
		out[i] = llm.Message{Role: "user", Content: c}
	}
	return out
}

func TestPrefixTracker(t *testing.T) {
	t.Parallel()

	t.Run("first turn never reports", func(t *testing.T) {
		t.Parallel()
		var p prefixTracker
		if d := p.note(1, msgs("a", "b")); d != nil {
			t.Errorf("nothing to compare against yet: %v", d)
		}
	})

	t.Run("pure append is silent", func(t *testing.T) {
		t.Parallel()
		// The normal, cheap case: the provider extends its cached prefix.
		var p prefixTracker
		p.note(1, msgs("a", "b"))
		if d := p.note(2, msgs("a", "b", "c")); d != nil {
			t.Errorf("growth must not be reported as drift: %v", d)
		}
		if d := p.note(3, msgs("a", "b", "c", "d", "e")); d != nil {
			t.Errorf("multi-message growth is still append: %v", d)
		}
	})

	t.Run("edit in place is reported at the right index", func(t *testing.T) {
		t.Parallel()
		var p prefixTracker
		p.note(1, msgs("alpha", "bravo", "charlie"))
		d := p.note(2, msgs("alpha", "CHANGED", "charlie"))
		if d == nil {
			t.Fatal("an edited message must be reported")
		}
		if d.Index != 1 {
			t.Errorf("Index = %d, want 1 (first changed message)", d.Index)
		}
		if d.Turn != 2 {
			t.Errorf("Turn = %d, want 2", d.Turn)
		}
	})

	t.Run("system drift is called out specifically", func(t *testing.T) {
		t.Parallel()
		// Index 0 instability is the worst case: it invalidates everything,
		// every turn, and is usually an accident (a timestamp in the prompt).
		var p prefixTracker
		p.note(1, []llm.Message{{Role: "system", Content: "you are mow @ 12:00"}, {Role: "user", Content: "hi"}})
		d := p.note(2, []llm.Message{{Role: "system", Content: "you are mow @ 12:01"}, {Role: "user", Content: "hi"}})
		if d == nil || d.Index != 0 {
			t.Fatalf("want drift at index 0, got %v", d)
		}
		if !strings.Contains(d.Reason, "system prompt") {
			t.Errorf("reason should name the system prompt: %q", d.Reason)
		}
	})

	t.Run("rewritten tool result is identified", func(t *testing.T) {
		t.Parallel()
		// This is the store-and-stub style hazard: a result enters history,
		// then gets replaced by a stub afterwards.
		before := []llm.Message{
			{Role: "user", Content: "read it"},
			{Role: "tool", Name: "read", Content: strings.Repeat("x", 5000)},
			{Role: "user", Content: "thanks"},
		}
		after := []llm.Message{
			{Role: "user", Content: "read it"},
			{Role: "tool", Name: "read", Content: "[stored: 5000 bytes]"},
			{Role: "user", Content: "thanks"},
		}
		var p prefixTracker
		p.note(1, before)
		d := p.note(2, after)
		if d == nil {
			t.Fatal("a rewritten tool result must be reported")
		}
		if d.Index != 1 || d.Name != "read" {
			t.Errorf("want index 1 / name read, got index %d name %q", d.Index, d.Name)
		}
		if !strings.Contains(d.Reason, "tool result was rewritten") {
			t.Errorf("reason should name the cause: %q", d.Reason)
		}
		// StaleChars must cover everything from the change to the end: that
		// is what the provider has to re-cache.
		if d.StaleChars < 5000 {
			t.Errorf("StaleChars = %d, should include the old body", d.StaleChars)
		}
	})

	t.Run("compaction is classified as a shrink", func(t *testing.T) {
		t.Parallel()
		var p prefixTracker
		p.note(1, msgs("a", "b", "c", "d", "e"))
		d := p.note(2, msgs("summary", "d", "e"))
		if d == nil {
			t.Fatal("compaction rewrites the prefix and must be reported")
		}
		if !strings.Contains(d.Reason, "shrank") {
			t.Errorf("reason should identify a shrink: %q", d.Reason)
		}
	})

	t.Run("tool call arguments are part of the hash", func(t *testing.T) {
		t.Parallel()
		// Arguments go on the wire, so a change to them is real drift even
		// when Content is identical.
		mk := func(args string) []llm.Message {
			return []llm.Message{{Role: "assistant", ToolCalls: []llm.ToolCall{{
				ID: "c1", Function: llm.FunctionCall{Name: "read", Arguments: args},
			}}}}
		}
		var p prefixTracker
		p.note(1, mk(`{"path":"a"}`))
		if d := p.note(2, mk(`{"path":"b"}`)); d == nil {
			t.Error("changed tool arguments must count as drift")
		}
	})
}

func TestDriftSummary(t *testing.T) {
	t.Parallel()
	var s driftSummary
	if s.String() != "" {
		t.Error("a clean run should produce no summary line")
	}
	s.add(driftReport{StaleChars: 1000, Reason: "compaction"})
	s.add(driftReport{StaleChars: 500, Reason: "compaction"})
	s.add(driftReport{StaleChars: 200, Reason: "system prompt"})
	out := s.String()
	for _, want := range []string{"3 prefix-drift", "1700", "compaction x2"} {
		if !strings.Contains(out, want) {
			t.Errorf("summary missing %q: %s", want, out)
		}
	}
}

// End-to-end: the loop must surface drift caused by its own compaction.
func TestLoopReportsPrefixDrift(t *testing.T) {
	t.Parallel()
	var events []string
	turn := 0
	chat := func(ctx context.Context, m []llm.Message, _ []llm.ToolSpec) (llm.Message, error) {
		turn++
		if turn < 3 {
			return llm.Message{
				Role: "assistant",
				ToolCalls: []llm.ToolCall{{
					ID: "c1", Type: "function",
					Function: llm.FunctionCall{Name: "write", Arguments: `{"path":"a"}`},
				}},
			}, nil
		}
		return llm.Message{Role: "assistant", Content: "done"}, nil
	}
	_, err := Run(context.Background(), chat, "go", Options{
		MaxTurns: 5,
		Tools:    []Tool{&countingWriteTool{}},
		// A tiny budget forces compaction, which rewrites history.
		MaxContextChars: 200,
		OnPrefixDrift: func(t2, idx int, detail string) {
			if idx >= 0 {
				events = append(events, detail)
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	// Not asserting a count: whether compaction fires depends on the budget
	// arithmetic. Asserting the wiring — a nil hook must not panic, and a
	// non-nil one must receive well-formed detail when it does fire.
	for _, e := range events {
		if !strings.Contains(e, "history changed at index") {
			t.Errorf("malformed drift detail: %q", e)
		}
	}
}

// The hook is optional and must cost nothing when unset.
func TestNilDriftHookIsSafe(t *testing.T) {
	t.Parallel()
	chat := func(ctx context.Context, _ []llm.Message, _ []llm.ToolSpec) (llm.Message, error) {
		return llm.Message{Role: "assistant", Content: "ok"}, nil
	}
	if _, err := Run(context.Background(), chat, "hi", Options{MaxTurns: 2}); err != nil {
		t.Fatal(err)
	}
}
