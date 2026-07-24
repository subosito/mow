package proc

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/subosito/mow"
)

func engCtx(t *testing.T, allowShell bool) (context.Context, *mow.Engine) {
	t.Helper()
	t.Setenv("MOW_HOME", t.TempDir())
	eng, err := mow.New(mow.Options{NoSession: true, AllowShell: allowShell,
		Chat: func(ctx context.Context, m []mow.Message, tl []mow.ToolSpec) (mow.Message, error) {
			return mow.Message{Role: "assistant", Content: "ok"}, nil
		}})
	if err != nil {
		t.Fatal(err)
	}
	return mow.ContextWithEngine(context.Background(), eng), eng
}

func TestProcToolsGatedByShell(t *testing.T) {
	// Without --allow-shell, start refuses.
	ctx, _ := engCtx(t, false)
	out, _ := startTool{}.Exec(ctx, json.RawMessage(`{"id":"x","command":"sleep 30"}`))
	if !strings.Contains(out, "allow-shell") {
		t.Fatalf("expected shell gate, got %q", out)
	}
}

func TestProcLifecycle(t *testing.T) {
	ctx, _ := engCtx(t, true)
	out, _ := startTool{}.Exec(ctx, json.RawMessage(`{"id":"srv","command":"sleep 30"}`))
	if !strings.Contains(out, "started id=srv") {
		t.Fatalf("start: %q", out)
	}
	st, _ := statusTool{}.Exec(ctx, json.RawMessage(`{"id":"srv"}`))
	if !strings.Contains(st, "status=running") {
		t.Fatalf("status: %q", st)
	}
	all, _ := statusTool{}.Exec(ctx, json.RawMessage(`{}`))
	if !strings.Contains(all, "srv") {
		t.Fatalf("list: %q", all)
	}
	sp, _ := stopTool{}.Exec(ctx, json.RawMessage(`{"id":"srv"}`))
	if !strings.Contains(sp, "stopped id=srv") {
		t.Fatalf("stop: %q", sp)
	}
	time.Sleep(150 * time.Millisecond)
	st2, _ := statusTool{}.Exec(ctx, json.RawMessage(`{"id":"srv"}`))
	if !strings.Contains(st2, "not found") {
		t.Fatalf("after stop (want not found): %q", st2)
	}
}

func TestAutoKillOnClose(t *testing.T) {
	ctx, eng := engCtx(t, true)
	if out, _ := (startTool{}).Exec(ctx, json.RawMessage(`{"id":"auto","command":"sleep 60"}`)); !strings.Contains(out, "started id=auto") {
		t.Fatalf("start: %q", out)
	}
	if st, _ := (statusTool{}).Exec(ctx, json.RawMessage(`{"id":"auto"}`)); !strings.Contains(st, "status=running") {
		t.Fatalf("should be running: %q", st)
	}
	// Closing the engine must kill the auto process.
	eng.Close()
	time.Sleep(200 * time.Millisecond)
	if st, _ := (statusTool{}).Exec(ctx, json.RawMessage(`{"id":"auto"}`)); !strings.Contains(st, "not found") {
		t.Fatalf("auto proc should be gone after Close: %q", st)
	}

	// keep=true survives Close.
	ctx2, eng2 := engCtx(t, true)
	if out, _ := (startTool{}).Exec(ctx2, json.RawMessage(`{"id":"kept","command":"sleep 60","keep":true}`)); !strings.Contains(out, "kept") {
		t.Fatalf("keep start: %q", out)
	}
	eng2.Close()
	time.Sleep(100 * time.Millisecond)
	if st, _ := (statusTool{}).Exec(ctx2, json.RawMessage(`{"id":"kept"}`)); !strings.Contains(st, "status=running") {
		t.Fatalf("kept proc should survive Close: %q", st)
	}
	_, _ = (stopTool{}).Exec(ctx2, json.RawMessage(`{"id":"kept"}`)) // cleanup
}
