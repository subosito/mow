package mow_test

import (
	"context"
	"strings"
	"testing"

	"github.com/subosito/mow"
)

// An ephemeral prompt runs against current context but leaves no trace: it must
// not appear in the transcript, and must not re-enter a later prompt's history.
func TestPromptEphemeralDoesNotPollute(t *testing.T) {
	var lastSeen []mow.Message
	eng, err := mow.New(mow.Options{
		NoSession: true,
		Chat: func(ctx context.Context, messages []mow.Message, tools []mow.ToolSpec) (mow.Message, error) {
			lastSeen = append([]mow.Message(nil), messages...)
			return mow.Message{Role: "assistant", Content: "reply"}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	if _, err := eng.Prompt(ctx, "first normal message"); err != nil {
		t.Fatal(err)
	}
	baseTranscript := len(eng.Transcript())

	// Ephemeral aside: sees the prior context...
	if _, err := eng.PromptWith(ctx, "ASIDE ephemeral question", mow.PromptOpts{Ephemeral: true}); err != nil {
		t.Fatal(err)
	}
	sawPrior := false
	for _, m := range lastSeen {
		if strings.Contains(m.Content, "first normal message") {
			sawPrior = true
		}
	}
	if !sawPrior {
		t.Fatal("ephemeral prompt should run against current context (prior message missing)")
	}

	// ...but leaves the transcript untouched.
	if got := len(eng.Transcript()); got != baseTranscript {
		t.Fatalf("ephemeral changed transcript: %d → %d", baseTranscript, got)
	}
	for _, m := range eng.Transcript() {
		if strings.Contains(m.Content, "ASIDE") {
			t.Fatalf("ephemeral aside leaked into transcript: %q", m.Content)
		}
	}

	// A later normal prompt must not see the aside in its history.
	if _, err := eng.Prompt(ctx, "second normal message"); err != nil {
		t.Fatal(err)
	}
	for _, m := range lastSeen {
		if strings.Contains(m.Content, "ASIDE") {
			t.Fatalf("aside re-entered a later prompt's context: %q", m.Content)
		}
	}
	// Sanity: the second normal prompt still saw the first (real history intact).
	sawFirst := false
	for _, m := range lastSeen {
		if strings.Contains(m.Content, "first normal message") {
			sawFirst = true
		}
	}
	if !sawFirst {
		t.Fatal("real history lost: second prompt did not see the first message")
	}
}
