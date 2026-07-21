package goal

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestProcessStartStatusStop(t *testing.T) {
	root := t.TempDir()
	ctx := withProcessScope(context.Background(), processScope{GoalID: "g1", Root: root})

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
