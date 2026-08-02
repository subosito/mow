package session_test

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/subosito/mow/internal/agent"
	"github.com/subosito/mow/internal/llm"
	"github.com/subosito/mow/internal/session"
)

func TestRunCompactionDoesNotRewriteSessionJSONL(t *testing.T) {
	s := &session.Store{Dir: t.TempDir(), ID: "projection"}
	messages := []llm.Message{{Role: "system", Content: "sys"}, {Role: "user", Content: "inspect"},
		{Role: "tool", Name: "read", Content: strings.Repeat("x", 12_000)}}
	for i := range messages {
		m := messages[i]
		if err := s.Append(session.Event{Type: "message", Message: &m}); err != nil {
			t.Fatal(err)
		}
	}
	before, err := os.ReadFile(s.Path())
	if err != nil {
		t.Fatal(err)
	}
	prior, err := s.LoadMessages()
	if err != nil {
		t.Fatal(err)
	}
	_, err = agent.Run(context.Background(), func(_ context.Context, got []llm.Message, _ []llm.ToolSpec) (llm.Message, error) {
		compacted := false
		for _, m := range got {
			if m.Role == "tool" && strings.Contains(m.Content, "…(snip)") {
				compacted = true
			}
		}
		if !compacted {
			t.Fatalf("projection was not snipped: %#v", got)
		}
		return llm.Message{Role: "assistant", Content: "done"}, nil
	}, "continue", agent.Options{PriorMessages: prior, MaxTurns: 1, MaxContextChars: 4_000})
	if err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(s.Path())
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("compaction rewrote append-only session JSONL")
	}
}
