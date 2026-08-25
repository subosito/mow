package session

import (
	"os"
	"strings"
	"testing"

	"github.com/subosito/mow/internal/llm"
)

func TestArchiveCompactRoundTrip(t *testing.T) {
	dir := t.TempDir()
	s := &Store{Dir: dir, ID: "20260101T000000"}
	path, err := s.ArchiveCompact([]llm.Message{
		{Role: "user", Content: "find the needle-alpha marker"},
		{Role: "assistant", Content: "ok"},
	}, "drop", 1200)
	if err != nil {
		t.Fatal(err)
	}
	if path == "" {
		t.Fatal("empty path")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "needle-alpha") {
		t.Fatalf("archive missing content: %s", b)
	}
	entries, err := os.ReadDir(s.ArchiveDir())
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("archive files=%v, want 1", entries)
	}
}
