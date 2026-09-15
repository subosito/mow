package engine

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/subosito/mow/internal/llm"
)

func TestSnapshotSizeDetectsChange(t *testing.T) {
	base := []llm.Message{{Role: "user", Content: "hi"}}
	n, size := snapshotSize(base)
	if n != 1 || size == 0 {
		t.Fatalf("snapshotSize=%d/%d", n, size)
	}
	if n2, s2 := snapshotSize(base); n2 != n || s2 != size {
		t.Fatalf("unstable for identical history: %d/%d", n2, s2)
	}
	// Same message count, different content → different size.
	edited := []llm.Message{{Role: "user", Content: "hi there"}}
	if _, s3 := snapshotSize(edited); s3 == size {
		t.Fatal("edited content must change the size")
	}
	// Tool calls count too (a turn can add only a call).
	withCall := []llm.Message{{Role: "user", Content: "hi", ToolCalls: []llm.ToolCall{{ID: "1"}}}}
	if _, s4 := snapshotSize(withCall); s4 == size {
		t.Fatal("tool calls must change the size")
	}
}

func TestSessionWritesOneSnapshotPerChangedTurn(t *testing.T) {
	home := t.TempDir()
	ws := t.TempDir()
	t.Setenv("MOW_HOME", home)

	eng, err := New(Options{
		LoadUserConfig: true,
		Workspace:      ws,
		Model:          "model-a",
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
		if _, err := eng.Prompt(context.Background(), p); err != nil {
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
	got := strings.Count(string(raw), `"type":"snapshot"`)
	if got != 2 {
		t.Fatalf("snapshot events = %d, want 2 (one per changed turn)\n%s", got, raw)
	}

	// An unchanged history (same counts and bytes as the last snapshot) is
	// skipped rather than appended again.
	eng.mu.Lock()
	n, size := eng.snapMsgs, eng.snapBytes
	eng.mu.Unlock()
	if n == 0 || size == 0 {
		t.Fatalf("snapshot signature not recorded: %d/%d", n, size)
	}
}
