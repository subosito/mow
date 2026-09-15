package engine

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSessionWritesOneReplayRecordPerChangedTurn(t *testing.T) {
	home := t.TempDir()
	ws := t.TempDir()
	t.Setenv("MOW_HOME", home)

	eng, err := New(Options{
		LoadUserConfig: true,
		Workspace:      ws,
		Model:          "gpt-5-mini",
		SessionID:      "sess-snap",
		Chat: func(context.Context, []Message, []ToolSpec) (Message, error) {
			return Message{Role: "assistant", Content: "ok"}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()
	for _, p := range []string{"one", "two"} {
		if _, err := eng.Prompt(t.Context(), p); err != nil {
			t.Fatal(err)
		}
	}

	var path string
	_ = filepath.WalkDir(home, func(p string, d os.DirEntry, _ error) error {
		if d != nil && !d.IsDir() && strings.HasSuffix(p, "sess-snap.jsonl") {
			path = p
		}
		return nil
	})
	if path == "" {
		t.Fatal("session file not found under MOW_HOME")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Count(string(raw), `"type":"snapshot"`) + strings.Count(string(raw), `"type":"delta"`)
	if got != 2 {
		t.Fatalf("replay records = %d, want 2 (one per changed turn)\n%s", got, raw)
	}
}
