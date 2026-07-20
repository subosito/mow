package session

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/subosito/mow/internal/llm"
)

func TestLatestID(t *testing.T) {
	dir := t.TempDir()
	if id, err := LatestID(dir); err != nil || id != "" {
		t.Fatalf("empty dir: id=%q err=%v", id, err)
	}
	a := filepath.Join(dir, "a.jsonl")
	b := filepath.Join(dir, "b.jsonl")
	if err := os.WriteFile(a, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	time.Sleep(10 * time.Millisecond)
	if err := os.WriteFile(b, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	id, err := LatestID(dir)
	if err != nil {
		t.Fatal(err)
	}
	if id != "b" {
		t.Fatalf("got %q want b", id)
	}
}

func TestLoadMessagesLastSnapshot(t *testing.T) {
	dir := t.TempDir()
	s := &Store{Dir: dir, ID: "t1"}
	// Turn 1
	if err := s.Append(Event{Type: "user", Role: "user", Content: "hi"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Append(Event{Type: "assistant", Role: "assistant", Content: "hello"}); err != nil {
		t.Fatal(err)
	}
	for _, m := range []llm.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "hi"},
		{Role: "assistant", Content: "hello"},
	} {
		mm := m
		if err := s.Append(Event{Type: "message", Message: &mm}); err != nil {
			t.Fatal(err)
		}
	}
	// Turn 2 — full dump again (would double if we naïvely concat)
	if err := s.Append(Event{Type: "user", Role: "user", Content: "again"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Append(Event{Type: "assistant", Role: "assistant", Content: "ok"}); err != nil {
		t.Fatal(err)
	}
	for _, m := range []llm.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "hi"},
		{Role: "assistant", Content: "hello"},
		{Role: "user", Content: "again"},
		{Role: "assistant", Content: "ok"},
	} {
		mm := m
		if err := s.Append(Event{Type: "message", Message: &mm}); err != nil {
			t.Fatal(err)
		}
	}

	msgs, err := s.LoadMessages()
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 5 {
		t.Fatalf("want last snapshot of 5 msgs, got %d: %+v", len(msgs), msgs)
	}
	if msgs[0].Role != "system" || msgs[4].Content != "ok" {
		t.Fatalf("snapshot wrong: %+v", msgs)
	}

	turns, err := s.LoadTranscript()
	if err != nil {
		t.Fatal(err)
	}
	if len(turns) != 4 {
		t.Fatalf("transcript want 4 user/asst, got %d: %+v", len(turns), turns)
	}
	if turns[0].Role != "user" || turns[0].Content != "hi" || turns[3].Content != "ok" {
		t.Fatalf("transcript: %+v", turns)
	}
}

func TestLoadTranscriptSkipsMessageDumps(t *testing.T) {
	dir := t.TempDir()
	s := &Store{Dir: dir, ID: "t2"}
	_ = s.Append(Event{Type: "user", Role: "user", Content: "u"})
	mm := llm.Message{Role: "user", Content: "should-not-appear-twice"}
	_ = s.Append(Event{Type: "message", Message: &mm})
	_ = s.Append(Event{Type: "assistant", Role: "assistant", Content: "a"})
	turns, err := s.LoadTranscript()
	if err != nil {
		t.Fatal(err)
	}
	if len(turns) != 2 || turns[0].Content != "u" || turns[1].Content != "a" {
		t.Fatalf("%+v", turns)
	}
}

func TestValidateID(t *testing.T) {
	for _, id := range []string{"20260718T101112", "abc-DEF_1.2", "s1"} {
		if err := ValidateID(id); err != nil {
			t.Errorf("ValidateID(%q) = %v, want nil", id, err)
		}
	}
	for _, id := range []string{"", "../../etc/passwd", "a/b", `a\b`, ".hidden", "..", "a b", "a\x00b"} {
		if err := ValidateID(id); err == nil {
			t.Errorf("ValidateID(%q) = nil, want error", id)
		}
	}
}

func TestLoadMessagesRepairsDanglingToolCalls(t *testing.T) {
	dir := t.TempDir()
	s := &Store{Dir: dir, ID: "t2"}
	// Snapshot from a cancelled run: assistant issued two tool calls, only one
	// result was recorded before the batch aborted.
	dump := []llm.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "do things"},
		{Role: "assistant", ToolCalls: []llm.ToolCall{
			{ID: "call_1", Type: "function", Function: llm.FunctionCall{Name: "read", Arguments: "{}"}},
			{ID: "call_2", Type: "function", Function: llm.FunctionCall{Name: "bash", Arguments: "{}"}},
		}},
		{Role: "tool", ToolCallID: "call_1", Name: "read", Content: "ok"},
	}
	for i := range dump {
		if err := s.Append(Event{Type: "message", Message: &dump[i]}); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.LoadMessages()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 5 {
		t.Fatalf("len=%d want 5 (synthesized result missing?): %+v", len(got), got)
	}
	syn := got[4]
	if syn.Role != "tool" || syn.ToolCallID != "call_2" {
		t.Fatalf("synthesized=%+v", syn)
	}
	if syn.Content == "" {
		t.Fatal("synthesized result should explain the interruption")
	}
	// A complete snapshot passes through untouched.
	answered := &llm.Message{Role: "tool", ToolCallID: "call_2", Name: "bash", Content: "done"}
	if err := s.Append(Event{Type: "message", Message: answered}); err != nil {
		t.Fatal(err)
	}
}
