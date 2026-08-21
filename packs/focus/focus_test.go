package focus_test

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

// Builtin names (read/edit/bash) cannot be replaced via Options.Tools, so
// these Engine tests drive the real builtins. Chat sequences match the
// pre-move agent.Run tests; results are read from eng.Messages().

type chatSeq struct {
	mu sync.Mutex
	n  int
}

func (s *chatSeq) next() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.n++
	return s.n
}

func newEngine(t *testing.T, opt mow.Options) *mow.Engine {
	t.Helper()
	t.Setenv("MOW_HOME", t.TempDir())
	if strings.TrimSpace(opt.Workspace) == "" {
		opt.Workspace = t.TempDir()
	}
	opt.NoSession = true
	if opt.MaxTurns == 0 {
		opt.MaxTurns = 10
	}
	if opt.Chat == nil {
		t.Fatal("Chat is required")
	}
	eng, err := mow.New(opt)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = eng.Close() })
	return eng
}

func toolOuts(eng *mow.Engine) []string {
	var out []string
	for _, m := range eng.Messages() {
		if m.Role == "tool" {
			out = append(out, m.Content)
		}
	}
	return out
}

func TestRereadShortCircuit(t *testing.T) {
	ws := t.TempDir()
	path := "port.go"
	if err := os.WriteFile(filepath.Join(ws, path), []byte("CONTENT:"+path), 0o600); err != nil {
		t.Fatal(err)
	}

	var seq chatSeq
	args, _ := json.Marshal(map[string]string{"path": path})
	chat := func(ctx context.Context, messages []mow.Message, tools []mow.ToolSpec) (mow.Message, error) {
		n := seq.next()
		if n > 2 {
			return mow.Message{Role: "assistant", Content: "done"}, nil
		}
		return mow.Message{
			Role: "assistant",
			ToolCalls: []mow.ToolCall{{
				ID:   fmt.Sprintf("c%d", n),
				Type: "function",
				Function: mow.FunctionCall{
					Name:      "read",
					Arguments: string(args),
				},
			}},
		}, nil
	}

	var mu sync.Mutex
	var readN int
	eng := newEngine(t, mow.Options{
		Workspace: ws,
		Chat:      chat,
		OnEvent: func(ev mow.Event) {
			if ev.Type == mow.EventToolEnd && ev.Tool == "read" {
				mu.Lock()
				readN++
				mu.Unlock()
			}
		},
	})
	if _, err := eng.Prompt(context.Background(), "hi"); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	got := readN
	mu.Unlock()
	if got != 2 {
		t.Fatalf("Exec count=%d want 2 (second read still runs, result degraded)", got)
	}
	outs := toolOuts(eng)
	if len(outs) != 2 {
		t.Fatalf("tool msgs=%d", len(outs))
	}
	if !strings.Contains(outs[0], "CONTENT:") {
		t.Fatalf("first=%q", outs[0])
	}
	if !strings.Contains(outs[1], "already read") {
		t.Fatalf("second=%q want already-read notice", outs[1])
	}
	if !strings.Contains(outs[1], "CONTENT:") {
		t.Fatalf("second=%q want live content behind the notice", outs[1])
	}
}

func TestRereadAllowedAfterEdit(t *testing.T) {
	ws := t.TempDir()
	path := "port.go"
	if err := os.WriteFile(filepath.Join(ws, path), []byte("CONTENT:"+path+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	seqCalls := []struct{ name, args string }{
		{"read", mustJSON(map[string]string{"path": path})},
		{"read", mustJSON(map[string]string{"path": path})},
		{"edit", mustJSON(map[string]string{
			"path": path, "old_string": "CONTENT:" + path, "new_string": "EDITED:" + path,
		})},
		{"read", mustJSON(map[string]string{"path": path})},
		{"read", mustJSON(map[string]string{"path": path})},
	}
	var seq chatSeq
	chat := func(ctx context.Context, messages []mow.Message, tools []mow.ToolSpec) (mow.Message, error) {
		n := seq.next()
		if n > len(seqCalls) {
			return mow.Message{Role: "assistant", Content: "done"}, nil
		}
		c := seqCalls[n-1]
		return mow.Message{
			Role: "assistant",
			ToolCalls: []mow.ToolCall{{
				ID:       fmt.Sprintf("c%d", n),
				Type:     "function",
				Function: mow.FunctionCall{Name: c.name, Arguments: c.args},
			}},
		}, nil
	}

	var mu sync.Mutex
	var readN, editN int
	eng := newEngine(t, mow.Options{
		Workspace:  ws,
		AllowWrite: true,
		Chat:       chat,
		OnEvent: func(ev mow.Event) {
			if ev.Type != mow.EventToolEnd {
				return
			}
			mu.Lock()
			defer mu.Unlock()
			switch ev.Tool {
			case "read":
				readN++
			case "edit":
				editN++
			}
		},
	})
	if _, err := eng.Prompt(context.Background(), "hi"); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	rn, en := readN, editN
	mu.Unlock()
	if rn != 4 {
		t.Fatalf("read Exec count=%d want 4 (repeats run; post-edit read allowed)", rn)
	}
	if en != 1 {
		t.Fatalf("edit Exec count=%d want 1", en)
	}
	outs := toolOuts(eng)
	if len(outs) != 5 {
		t.Fatalf("tool msgs=%d want 5: %q", len(outs), outs)
	}
	if !strings.Contains(outs[0], "CONTENT:") {
		t.Fatalf("first read=%q", outs[0])
	}
	if !strings.Contains(outs[1], "already read") {
		t.Fatalf("second read=%q want already-read notice", outs[1])
	}
	if !strings.Contains(outs[1], "CONTENT:") {
		t.Fatalf("second read=%q want live content behind the notice", outs[1])
	}
	if strings.Contains(outs[2], "already read") {
		t.Fatalf("edit=%q", outs[2])
	}
	if !strings.Contains(outs[3], "EDITED:") {
		t.Fatalf("post-edit read=%q want live content", outs[3])
	}
	if strings.Contains(outs[3], "already read") {
		t.Fatalf("post-edit read=%q want no degrade notice", outs[3])
	}
	if !strings.Contains(outs[4], "already read") {
		t.Fatalf("second post-edit read=%q want already-read notice", outs[4])
	}
	if !strings.Contains(outs[4], "EDITED:") {
		t.Fatalf("second post-edit read=%q want live content behind the notice", outs[4])
	}
}

func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(b)
}
