package engine_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/subosito/mow"

	_ "github.com/subosito/mow/packs/focus"
)

// Second read of the same path must still Exec. The focus pack degrades the
// body; it must not set harness.tool.end denied (mowi paints that as
// "✗ read path: denied").
func TestRereadDoesNotEmitDenied(t *testing.T) {
	t.Setenv("MOW_HOME", t.TempDir())
	t.Setenv("OPENAI_API_KEY", "k")
	t.Setenv("OPENAI_MODEL", "m")

	ws := t.TempDir()
	path := filepath.Join(ws, "note.txt")
	if err := os.WriteFile(path, []byte("hello from disk"), 0o600); err != nil {
		t.Fatal(err)
	}

	n := 0
	var mu sync.Mutex
	var ends []mow.Event
	eng, err := mow.New(mow.Options{
		Workspace:      ws,
		NoSession:      true,
		LoadUserConfig: true,
		Chat: func(ctx context.Context, messages []mow.Message, tools []mow.ToolSpec) (mow.Message, error) {
			n++
			if n > 2 {
				return mow.Message{Role: "assistant", Content: "done"}, nil
			}
			args, _ := json.Marshal(map[string]string{"path": path})
			return mow.Message{Role: "assistant", ToolCalls: []mow.ToolCall{{
				ID:   fmt.Sprintf("c%d", n),
				Type: "function",
				Function: mow.FunctionCall{
					Name:      "read",
					Arguments: string(args),
				},
			}}}, nil
		},
		OnEvent: func(ev mow.Event) {
			if ev.Type == mow.EventToolEnd && ev.Tool == "read" {
				mu.Lock()
				ends = append(ends, ev)
				mu.Unlock()
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = eng.Close() })

	if _, err := eng.Prompt(context.Background(), "read the note twice"); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(ends) != 2 {
		t.Fatalf("tool.end count=%d want 2: %+v", len(ends), ends)
	}
	for i, ev := range ends {
		if ev.Denied {
			t.Fatalf("read %d denied=true error=%q result=%q", i+1, ev.Error, ev.Result)
		}
	}
	if !strings.Contains(ends[0].Result, "hello from disk") {
		t.Fatalf("first result=%q", ends[0].Result)
	}
	if !strings.Contains(ends[1].Result, "hello from disk") {
		t.Fatalf("second result missing content: %q", ends[1].Result)
	}
}
