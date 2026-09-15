package goal

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/subosito/mow"
)

// shellEngine builds a hermetic engine with the given shell permission and
// returns a step context carrying it plus the goal process scope.
func shellEngine(t *testing.T, allowShell bool, root string) context.Context {
	t.Helper()
	t.Setenv("MOW_HOME", t.TempDir())
	t.Setenv("OPENAI_API_KEY", "k")
	t.Setenv("OPENAI_MODEL", "m")
	eng, err := mow.New(mow.Options{
		Workspace:  t.TempDir(),
		NoSession:  true,
		AllowShell: allowShell,
		Chat: func(ctx context.Context, messages []mow.Message, tools []mow.ToolSpec) (mow.Message, error) {
			return mow.Message{Role: "assistant", Content: "ok"}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = eng.Close() })
	ctx := mow.ContextWithEngine(context.Background(), eng)
	return withProcessScope(ctx, processScope{GoalID: "g1", Root: root})
}

func TestProcessStartRequiresAllowShell(t *testing.T) {
	root := t.TempDir()
	args := json.RawMessage(`{"id":"s","command":"sleep 30"}`)

	// No engine on the context: refuse.
	ctx := withProcessScope(context.Background(), processScope{GoalID: "g1", Root: root})
	out, err := procStartTool{}.Exec(ctx, args)
	if err != nil || !strings.Contains(out, "engine context") {
		t.Fatalf("no engine: out=%q err=%v", out, err)
	}
	// Engine without --allow-shell: refuse, nothing started.
	out, err = procStartTool{}.Exec(shellEngine(t, false, root), args)
	if err != nil || !strings.Contains(out, "--allow-shell") {
		t.Fatalf("no shell: out=%q err=%v", out, err)
	}
	if _, err := os.Stat(filepath.Join(procDir(root, "g1"), "s.pid")); err == nil {
		t.Fatal("process started without --allow-shell")
	}
}

func TestProcessStartStatusStop(t *testing.T) {
	root := t.TempDir()
	ctx := shellEngine(t, true, root)

	// Start a short sleep loop that stays alive.
	args, _ := json.Marshal(map[string]string{
		"id":      "sleeper",
		"command": "while true; do sleep 30; done",
	})
	out, err := procStartTool{}.Exec(ctx, args)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "started") && !strings.Contains(out, "running") {
		t.Fatalf("start=%q", out)
	}
	st, err := procStatusTool{}.Exec(ctx, json.RawMessage(`{"id":"sleeper"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(st, "running") {
		t.Fatalf("status=%q", st)
	}
	// pid file exists
	if _, err := os.Stat(filepath.Join(procDir(root, "g1"), "sleeper.pid")); err != nil {
		t.Fatal(err)
	}
	stop, err := procStopTool{}.Exec(ctx, json.RawMessage(`{"id":"sleeper"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stop, "stopped") {
		t.Fatalf("stop=%q", stop)
	}
	// Allow reaping.
	time.Sleep(100 * time.Millisecond)
	st2, _ := procStatusTool{}.Exec(ctx, json.RawMessage(`{"id":"sleeper"}`))
	if strings.Contains(st2, "running") {
		t.Fatalf("still running: %q", st2)
	}
}
