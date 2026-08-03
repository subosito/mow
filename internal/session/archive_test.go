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
	files, err := s.ArchiveFiles()
	if err != nil || len(files) != 1 {
		t.Fatalf("files=%v err=%v", files, err)
	}
}
