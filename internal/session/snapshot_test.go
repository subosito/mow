package session

import (
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/subosito/mow/internal/llm"
)

func TestSnapshotContentAndDeltas(t *testing.T) {
	s := &Store{Dir: t.TempDir(), ID: "replay"}
	messages := []llm.Message{{Role: "system", Content: strings.Repeat("s", 1000)}, {Role: "user", Content: "one"}}
	check := func(want []llm.Message) {
		t.Helper()
		got, err := s.LoadMessages()
		if err != nil {
			t.Fatal(err)
		}
		g, _ := json.Marshal(got)
		w, _ := json.Marshal(want)
		if string(g) != string(w) {
			t.Fatalf("replayed %s, want %s", g, w)
		}
	}
	appendSnapshot := func() {
		t.Helper()
		if err := s.AppendSnapshot(messages); err != nil {
			t.Fatal(err)
		}
		check(messages)
	}
	appendSnapshot()
	before, _ := os.ReadFile(s.Path())
	appendSnapshot()
	after, _ := os.ReadFile(s.Path())
	if string(before) != string(after) {
		t.Fatal("identical snapshot appended again")
	}
	// Runtime/transcript appends must retain the base for the next delta.
	if err := s.Append(Event{Type: "user", Role: "user", Content: "two"}); err != nil {
		t.Fatal(err)
	}
	messages = append(messages, llm.Message{Role: "assistant", Content: "reply"})
	appendSnapshot()
	raw, _ := os.ReadFile(s.Path())
	if !strings.Contains(string(raw), `"type":"delta"`) {
		t.Fatal("unchanged prefix did not produce a delta")
	}
	// Equal lengths do not imply equal content, including provider metadata.
	messages[1].Content = "two"
	appendSnapshot()
	messages = append(messages, llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "c", ThoughtSignature: "one"}}},
		llm.Message{Role: "tool", ToolCallID: "c", Content: "ok"})
	appendSnapshot()
	messages[3].ToolCalls[0].ThoughtSignature = "two"
	appendSnapshot()
	// A compact/rewind is a replacement checkpoint.
	messages = messages[:2]
	appendSnapshot()
}

func TestSnapshotWriterChange(t *testing.T) {
	dir := t.TempDir()
	a := &Store{Dir: dir, ID: "shared"}
	b := &Store{Dir: dir, ID: "shared"}
	first := []llm.Message{{Role: "system", Content: strings.Repeat("s", 1000)}, {Role: "user", Content: "first"}}
	if err := a.AppendSnapshot(first); err != nil {
		t.Fatal(err)
	}
	other := []llm.Message{{Role: "system", Content: "other"}, {Role: "user", Content: "second writer"}}
	if err := b.AppendSnapshot(other); err != nil {
		t.Fatal(err)
	}
	// Even an identical history must be persisted after another writer's turn.
	if err := a.AppendSnapshot(first); err != nil {
		t.Fatal(err)
	}
	got, err := b.LoadMessages()
	if err != nil || len(got) != 2 || got[1].Content != "first" {
		t.Fatalf("writer change replay = %+v, %v", got, err)
	}
}

func TestSnapshotRetryAfterAppendFailure(t *testing.T) {
	s := &Store{Dir: t.TempDir(), ID: "retry"}
	messages := []llm.Message{{Role: "system", Content: "sys"}, {Role: "user", Content: "one"}}
	if err := s.AppendSnapshot(messages); err != nil {
		t.Fatal(err)
	}
	path := s.Path()
	if err := os.Rename(path, path+".saved"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	messages[1].Content = "two"
	if err := s.AppendSnapshot(messages); err == nil {
		t.Fatal("expected append failure")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path+".saved", path); err != nil {
		t.Fatal(err)
	}
	if err := s.AppendSnapshot(messages); err != nil {
		t.Fatal(err)
	}
	got, err := s.LoadMessages()
	if err != nil || len(got) != 2 || got[1].Content != "two" {
		t.Fatalf("retry replay = %+v, %v", got, err)
	}
}

func TestSnapshotStorageGrowth(t *testing.T) {
	s := &Store{Dir: t.TempDir(), ID: "growth"}
	messages := []llm.Message{{Role: "system", Content: "sys"}}
	for range 200 {
		messages = append(messages, llm.Message{Role: "user", Content: strings.Repeat("u", 1024)}, llm.Message{Role: "assistant", Content: strings.Repeat("a", 1024)})
		if err := s.AppendSnapshot(messages); err != nil {
			t.Fatal(err)
		}
	}
	raw, err := os.ReadFile(s.Path())
	if err != nil {
		t.Fatal(err)
	}
	history, _ := json.Marshal(messages)
	if len(raw) > 4*len(history) {
		t.Fatalf("session grew to %d bytes for %d bytes of history", len(raw), len(history))
	}
	t.Logf("200 turns: %d bytes on disk; %d bytes of final history", len(raw), len(history))
	got, err := s.LoadMessages()
	if err != nil || len(got) != len(messages) {
		t.Fatalf("replay count=%d want %d: %v", len(got), len(messages), err)
	}
}

func TestSnapshotCrashTailAndInvalidBase(t *testing.T) {
	s := &Store{Dir: t.TempDir(), ID: "crash"}
	messages := []llm.Message{{Role: "system", Content: strings.Repeat("s", 70_000)}, {Role: "user", Content: "one"}}
	if err := s.AppendSnapshot(messages); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(s.Path(), os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.WriteString(`{"type":"snapshot","messages":[`)
	_ = f.Close()
	if err != nil {
		t.Fatal(err)
	}
	messages = append(slices.Clone(messages), llm.Message{Role: "assistant", Content: "recovered"})
	if err := s.AppendSnapshot(messages); err != nil {
		t.Fatal(err)
	}
	got, err := s.LoadMessages()
	if err != nil || len(got) != 3 || got[2].Content != "recovered" {
		t.Fatalf("recovered replay = %d messages, %v", len(got), err)
	}
	if err := s.Append(Event{Type: "delta", Base: new(int64(-1)), Messages: []llm.Message{{Role: "user", Content: "wrong base"}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.LoadMessages(); err == nil {
		t.Fatal("delta with a mismatched base accepted")
	}
}
