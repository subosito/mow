package mow_test

import (
	"context"
	"strings"
	"testing"

	"github.com/subosito/mow"
)

func TestRewind(t *testing.T) {
	var lastSeen []mow.Message
	eng, err := mow.New(mow.Options{
		NoSession: true,
		Chat: func(ctx context.Context, msgs []mow.Message, tools []mow.ToolSpec) (mow.Message, error) {
			lastSeen = append([]mow.Message(nil), msgs...)
			return mow.Message{Role: "assistant", Content: "answer"}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	eng.Prompt(ctx, "first question")
	eng.Prompt(ctx, "typo questiom")
	before := len(eng.Transcript())

	last, ok := eng.Rewind()
	if !ok || last != "typo questiom" {
		t.Fatalf("rewind=(%q,%v)", last, ok)
	}
	if got := len(eng.Transcript()); got != before-2 {
		t.Fatalf("transcript %d → %d, want -2", before, got)
	}
	// Re-prompt the corrected text: the rewound turn must be gone from context.
	eng.Prompt(ctx, "fixed question")
	for _, m := range lastSeen {
		if strings.Contains(m.Content, "typo") {
			t.Fatalf("rewound turn still in context: %q", m.Content)
		}
	}
	sawFirst := false
	for _, m := range lastSeen {
		if strings.Contains(m.Content, "first question") {
			sawFirst = true
		}
	}
	if !sawFirst {
		t.Fatal("kept turns lost after rewind")
	}
}
