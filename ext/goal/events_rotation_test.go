package goal

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAppendEventRotation(t *testing.T) {
	root := t.TempDir()
	s := &Store{Dir: filepath.Join(root, "goals")}
	path := filepath.Join(root, "goals", "rotate", "events.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.Repeat("x", maxEventsJSONLBytes)), 0o600); err != nil {
		t.Fatal(err)
	}
	s.AppendEvent("rotate", LogEvent{Kind: "step", Text: "after-rotate"})
	if _, err := os.Stat(path + ".1"); err != nil {
		t.Fatalf("expected rotated segment: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "after-rotate") {
		t.Fatalf("new segment missing append: %q", raw)
	}
}
