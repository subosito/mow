package mow_test

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/subosito/mow"
)

// Manual compaction rewrites the stored transcript via the tiered machinery:
// a follow-up prompt sees fewer history messages, and a loop.compact event
// reports the layer + savings.
func TestEngineCompactShrinksTranscript(t *testing.T) {
	var (
		mu    sync.Mutex
		sizes []int
	)
	eng, err := mow.New(mow.Options{
		NoSession: true,
		Chat: func(ctx context.Context, messages []mow.Message, tools []mow.ToolSpec) (mow.Message, error) {
			mu.Lock()
			sizes = append(sizes, len(messages))
			mu.Unlock()
			return mow.Message{Role: "assistant", Content: "ok"}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	var compactEvents int
	var saved int
	eng.AddOnEvent(func(ev mow.Event) {
		if ev.Type == mow.EventCompact {
			compactEvents++
			saved = ev.CharsSaved
		}
	})

	// Build a long history of small turns (20 x 500 chars ≈ 10k of user
	// content + ~10k system): older turns fall outside the pin window so the
	// drop layer can trim them.
	for i := 0; i < 20; i++ {
		if _, err := eng.Prompt(context.Background(), strings.Repeat("x", 500)); err != nil {
			t.Fatal(err)
		}
	}

	// Budget must be realistic: e.prior includes the system prompt (~10k
	// chars), which compaction never drops — a target below it is OverBudget
	// and keeps everything by design. 12k fits system + one pinned user turn
	// and drops the older 2k-char turns.
	rep, err := eng.Compact(12000)
	if err != nil {
		t.Fatal(err)
	}
	if compactEvents != 1 {
		t.Fatalf("compact events=%d want 1", compactEvents)
	}
	if rep.MessagesAfter >= rep.MessagesBefore || rep.MessagesAfter <= 0 {
		t.Fatalf("history did not shrink: %+v", rep)
	}
	if saved <= 0 {
		t.Fatalf("expected chars saved, got %d", saved)
	}
	// Layer must be a known tier.
	if rep.Layer != "snip" && rep.Layer != "drop" {
		t.Fatalf("unexpected layer %q", rep.Layer)
	}

	// THE REAL IMPACT: the next prompt's history comes from e.prior, so a
	// follow-up call must send fewer messages than the last seeding call.
	lastSeed := sizes[len(sizes)-1]
	if _, err := eng.Prompt(context.Background(), "after compact"); err != nil {
		t.Fatal(err)
	}
	after := sizes[len(sizes)-1]
	if after >= lastSeed {
		t.Fatalf("follow-up prompt sent %d messages, want < %d (compaction had no wire impact)", after, lastSeed)
	}
}

// Compact on an empty transcript is a no-op, not an error.
func TestEngineCompactEmptyNoop(t *testing.T) {
	eng, err := mow.New(mow.Options{
		NoSession: true,
		Chat: func(ctx context.Context, messages []mow.Message, tools []mow.ToolSpec) (mow.Message, error) {
			return mow.Message{Role: "assistant", Content: "ok"}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	rep, err := eng.Compact(100)
	if err != nil {
		t.Fatal(err)
	}
	if rep.MessagesBefore != 0 || rep.CharsSaved != 0 {
		t.Fatalf("empty compact should be a no-op: %+v", rep)
	}
}

// Compact(0) is what hosts call for /compact. It must free headroom even when
// history is still under the soft ceiling (default 100k or a large scaled
// window). A high floor that sat above cur made this a pure no-op.
// Target is ~20% of non-system body (not ~50% of total).
func TestEngineCompactZeroFreesHeadroom(t *testing.T) {
	eng, err := mow.New(mow.Options{
		NoSession: true,
		Chat: func(ctx context.Context, messages []mow.Message, tools []mow.ToolSpec) (mow.Message, error) {
			return mow.Message{Role: "assistant", Content: "ok",
				Usage: mow.Usage{InputTokens: 20_000, OutputTokens: 5}}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 40; i++ {
		prompt := "please fix bug number " + strings.Repeat("x", 200)
		if _, err := eng.Prompt(context.Background(), prompt); err != nil {
			t.Fatal(err)
		}
	}
	before := len(eng.Messages())
	rep, err := eng.Compact(0)
	if err != nil {
		t.Fatal(err)
	}
	after := len(eng.Messages())
	if rep.CharsSaved <= 0 {
		t.Fatalf("Compact(0) saved nothing: %+v msgs %d→%d", rep, before, after)
	}
	if after >= before {
		t.Fatalf("message count did not shrink: %d→%d (rep=%+v)", before, after, rep)
	}
	// Aggressive /compact keeps ~15% of non-system body; the system prompt is
	// never dropped, so total remaining% can look high on short fixtures.
	// Assert the recent tail collapses hard (40 turns → a handful of msgs).
	if after > before/5 {
		t.Fatalf("message count not aggressive enough: %d→%d (rep=%+v)", before, after, rep)
	}
	if rep.Layer != "drop" {
		t.Fatalf("want drop layer for multi-turn /compact, got %q", rep.Layer)
	}
}

// Large MaxContextChars (as when gateway context_window scales the soft budget
// to millions of chars) must not prevent /compact from dropping old turns.
func TestEngineCompactZeroWithLargeSoftBudget(t *testing.T) {
	eng, err := mow.New(mow.Options{
		NoSession:       true,
		MaxContextChars: 3_200_000,
		Chat: func(ctx context.Context, messages []mow.Message, tools []mow.ToolSpec) (mow.Message, error) {
			return mow.Message{Role: "assistant", Content: "ok",
				Usage: mow.Usage{InputTokens: 5_000, OutputTokens: 5}}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 30; i++ {
		if _, err := eng.Prompt(context.Background(), "work on feature "+strings.Repeat("y", 400)); err != nil {
			t.Fatal(err)
		}
	}
	before := len(eng.Messages())
	rep, err := eng.Compact(0)
	if err != nil {
		t.Fatal(err)
	}
	after := len(eng.Messages())
	if rep.CharsSaved <= 0 {
		t.Fatalf("Compact(0) with large soft budget saved nothing: %+v", rep)
	}
	if after >= before {
		t.Fatalf("message count did not shrink under large budget: %d→%d", before, after)
	}
}
