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

	// Build a history of several turns with heavy content.
	for i := 0; i < 5; i++ {
		if _, err := eng.Prompt(context.Background(), strings.Repeat("user turn ", 200)); err != nil {
			t.Fatal(err)
		}
	}

	rep, err := eng.Compact(300)
	if err != nil {
		t.Fatal(err)
	}
	if compactEvents != 1 {
		t.Fatalf("compact events=%d want 1", compactEvents)
	}
	if rep.MessagesAfter >= rep.MessagesBefore || rep.MessagesAfter <= 0 {
		t.Fatalf("transcript did not shrink: %+v", rep)
	}
	if saved <= 0 {
		t.Fatalf("expected chars saved, got %d", saved)
	}
	// Layer must be a known tier.
	if rep.Layer != "snip" && rep.Layer != "drop" {
		t.Fatalf("unexpected layer %q", rep.Layer)
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
