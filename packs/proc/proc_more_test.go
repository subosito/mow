package proc

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/subosito/mow/ext"
)

// --- storeDir -----------------------------------------------------------

func TestStoreDir(t *testing.T) {
	t.Parallel()
	base := storeDir("/some/workspace")
	if base == "" || !strings.Contains(base, "proc") {
		t.Fatalf("unexpected store dir %q", base)
	}
	// Deterministic for the same workspace.
	if got := storeDir("/some/workspace"); got != base {
		t.Fatalf("not deterministic: %q vs %q", got, base)
	}
	// Whitespace-padded input resolves to the same dir.
	if got := storeDir("  /some/workspace  "); got != base {
		t.Fatalf("trim mismatch: %q vs %q", got, base)
	}
	// Different workspaces never collide.
	if got := storeDir("/other/workspace"); got == base {
		t.Fatalf("collision: %q", got)
	}
}

// --- tool metadata ------------------------------------------------------

func TestToolMetadata(t *testing.T) {
	t.Parallel()
	tools := []ext.Tool{startTool{}, statusTool{}, stopTool{}}
	seen := map[string]bool{}
	for _, tl := range tools {
		if tl.Name() == "" {
			t.Fatal("empty tool name")
		}
		if seen[tl.Name()] {
			t.Fatalf("duplicate tool name %q", tl.Name())
		}
		seen[tl.Name()] = true
		if tl.Description() == "" {
			t.Fatalf("%s: empty description", tl.Name())
		}
		var schema map[string]any
		if err := json.Unmarshal(tl.Parameters(), &schema); err != nil {
			t.Fatalf("%s: parameters not valid JSON: %v", tl.Name(), err)
		}
		if schema["type"] != "object" {
			t.Fatalf("%s: schema type = %v", tl.Name(), schema["type"])
		}
	}
	if !(statusTool{}).ReadOnly() {
		t.Fatal("proc_status should be read-only")
	}
	if (startTool{}).Name() != "proc_start" || (stopTool{}).Name() != "proc_stop" {
		t.Fatal("unexpected tool names")
	}
}

// --- gating -------------------------------------------------------------

func TestToolsNeedEngineContext(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	for _, tc := range []struct {
		name string
		tool ext.Tool
		args string
	}{
		{"start", startTool{}, `{"id":"x","command":"sleep 1"}`},
		{"status", statusTool{}, `{}`},
		{"stop", stopTool{}, `{"id":"x"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := tc.tool.Exec(ctx, json.RawMessage(tc.args))
			if err != nil {
				t.Fatalf("err: %v", err)
			}
			if !strings.Contains(out, "engine context") {
				t.Fatalf("want engine-context error, got %q", out)
			}
		})
	}
}

func TestStatusAndStopGatedByShell(t *testing.T) {
	ctx, _ := engCtx(t, false)
	for _, tc := range []struct {
		tool ext.Tool
		args string
	}{
		{statusTool{}, `{}`},
		{stopTool{}, `{"id":"x"}`},
	} {
		out, _ := tc.tool.Exec(ctx, json.RawMessage(tc.args))
		if !strings.Contains(out, "allow-shell") {
			t.Fatalf("%s: expected shell gate, got %q", tc.tool.Name(), out)
		}
	}
}

// --- start edge cases ---------------------------------------------------

func TestStartInvalidArgs(t *testing.T) {
	ctx, _ := engCtx(t, true)
	if _, err := (startTool{}).Exec(ctx, json.RawMessage(`{not json`)); err == nil {
		t.Fatal("expected unmarshal error")
	}
	if _, err := (stopTool{}).Exec(ctx, json.RawMessage(`[1,2]`)); err == nil {
		t.Fatal("expected unmarshal error")
	}
}

func TestStartAlreadyRunning(t *testing.T) {
	ctx, _ := engCtx(t, true)
	out, _ := startTool{}.Exec(ctx, json.RawMessage(`{"id":"dup","command":"sleep 30"}`))
	if !strings.Contains(out, "started id=dup") {
		t.Fatalf("first start: %q", out)
	}
	out, _ = startTool{}.Exec(ctx, json.RawMessage(`{"id":"dup","command":"sleep 30"}`))
	if !strings.Contains(out, "already running id=dup") {
		t.Fatalf("second start: %q", out)
	}
	_, _ = stopTool{}.Exec(ctx, json.RawMessage(`{"id":"dup"}`))
}

func TestStartFastExit(t *testing.T) {
	ctx, _ := engCtx(t, true)
	// A command that exits right away: start still succeeds. Depending on
	// timing/OS the 200ms settle may or may not observe the exit (a released
	// child lingers as a zombie on Linux), so accept either message — but the
	// process record must be stoppable and gone afterwards.
	out, _ := startTool{}.Exec(ctx, json.RawMessage(`{"id":"quick","command":"exit 0"}`))
	if !strings.Contains(out, "started id=quick") {
		t.Fatalf("start: %q", out)
	}
	sp, _ := stopTool{}.Exec(ctx, json.RawMessage(`{"id":"quick"}`))
	if !strings.Contains(sp, "stopped id=quick") {
		t.Fatalf("stop: %q", sp)
	}
	st, _ := statusTool{}.Exec(ctx, json.RawMessage(`{"id":"quick"}`))
	if !strings.Contains(st, "not found") {
		t.Fatalf("after stop: %q", st)
	}
}

func TestStartSanitizesID(t *testing.T) {
	ctx, _ := engCtx(t, true)
	// Slashes/spaces are stripped; the sanitized id survives round-trip.
	out, _ := startTool{}.Exec(ctx, json.RawMessage(`{"id":"my srv/../x","command":"sleep 30"}`))
	if !strings.Contains(out, "started id=mysrvx") {
		t.Fatalf("start: %q", out)
	}
	_, _ = stopTool{}.Exec(ctx, json.RawMessage(`{"id":"mysrvx"}`))
}

// --- status / stop error paths ------------------------------------------

func TestStatusUnknownID(t *testing.T) {
	ctx, _ := engCtx(t, true)
	st, _ := statusTool{}.Exec(ctx, json.RawMessage(`{"id":"ghost"}`))
	if !strings.Contains(st, "not found") {
		t.Fatalf("status: %q", st)
	}
}

func TestStatusEmptyStore(t *testing.T) {
	ctx, _ := engCtx(t, true)
	all, _ := statusTool{}.Exec(ctx, json.RawMessage(`{}`))
	if all != "(no background processes)" {
		t.Fatalf("list: %q", all)
	}
}

func TestStopErrors(t *testing.T) {
	ctx, _ := engCtx(t, true)
	// Missing id.
	out, _ := stopTool{}.Exec(ctx, json.RawMessage(`{}`))
	if !strings.Contains(out, "id required") {
		t.Fatalf("missing id: %q", out)
	}
	// Id that sanitizes to empty.
	out, _ = stopTool{}.Exec(ctx, json.RawMessage(`{"id":"///"}`))
	if !strings.Contains(out, "id required") {
		t.Fatalf("blank id: %q", out)
	}
	// Unknown id.
	out, _ = stopTool{}.Exec(ctx, json.RawMessage(`{"id":"ghost"}`))
	if !strings.Contains(out, "not found") {
		t.Fatalf("unknown id: %q", out)
	}
}

// --- log writing and reading --------------------------------------------

func TestLogWritingAndTail(t *testing.T) {
	ctx, eng := engCtx(t, true)
	// Default log name (<id>.log) — status includes a tail of it.
	out, _ := startTool{}.Exec(ctx, json.RawMessage(`{"id":"logger","command":"echo line-one; echo line-two; sleep 30"}`))
	if !strings.Contains(out, "started id=logger") {
		t.Fatalf("start: %q", out)
	}
	st, _ := statusTool{}.Exec(ctx, json.RawMessage(`{"id":"logger"}`))
	if !strings.Contains(st, "status=running") || !strings.Contains(st, "log (tail)") {
		t.Fatalf("status: %q", st)
	}
	for _, want := range []string{"line-one", "line-two"} {
		if !strings.Contains(st, want) {
			t.Fatalf("status tail missing %q: %q", want, st)
		}
	}
	// The log file lives in the per-workspace store dir.
	dir := storeDir(eng.Workspace())
	raw, err := os.ReadFile(filepath.Join(dir, "logger.log"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "line-one") {
		t.Fatalf("log file: %q", string(raw))
	}
	_, _ = stopTool{}.Exec(ctx, json.RawMessage(`{"id":"logger"}`))

	// Custom log name is honored on disk and reported by start.
	out, _ = startTool{}.Exec(ctx, json.RawMessage(`{"id":"custom","command":"echo hello-custom; sleep 30","log":"custom.log"}`))
	if !strings.Contains(out, "started id=custom") || !strings.Contains(out, "custom.log") {
		t.Fatalf("custom start: %q", out)
	}
	raw, err = os.ReadFile(filepath.Join(dir, "custom.log"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "hello-custom") {
		t.Fatalf("custom log: %q", string(raw))
	}
	_, _ = stopTool{}.Exec(ctx, json.RawMessage(`{"id":"custom"}`))
}

// --- concurrent access ---------------------------------------------------

func TestConcurrentLifecycle(t *testing.T) {
	ctx, _ := engCtx(t, true)
	const n = 6
	ids := make([]string, n)
	for i := range ids {
		ids[i] = fmt.Sprintf("conc-%d", i)
	}

	// Start n processes concurrently — distinct ids must not interfere.
	var wg sync.WaitGroup
	starts := make([]string, n)
	for i := range ids {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			args, _ := json.Marshal(map[string]any{"id": ids[i], "command": "sleep 30"})
			starts[i], _ = startTool{}.Exec(ctx, args)
		}(i)
	}
	wg.Wait()
	for i, out := range starts {
		if !strings.Contains(out, "started id="+ids[i]) {
			t.Fatalf("start %d: %q", i, out)
		}
	}

	// Status reads in parallel.
	for i := range ids {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			st, _ := statusTool{}.Exec(ctx, json.RawMessage(`{"id":"`+ids[i]+`"}`))
			if !strings.Contains(st, "status=running") {
				t.Errorf("status %d: %q", i, st)
			}
		}(i)
	}
	wg.Wait()

	// Stop all in parallel.
	for i := range ids {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			sp, _ := stopTool{}.Exec(ctx, json.RawMessage(`{"id":"`+ids[i]+`"}`))
			if !strings.Contains(sp, "stopped id="+ids[i]) {
				t.Errorf("stop %d: %q", i, sp)
			}
		}(i)
	}
	wg.Wait()

	waitStatus(t, ctx, `{}`, "(no background processes)")
}

// --- procState ------------------------------------------------------------

func TestProcState(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		alive bool
		want  string
	}{
		{true, "running"},
		{false, "dead"},
	} {
		if got := procState(tc.alive); got != tc.want {
			t.Fatalf("procState(%v) = %q, want %q", tc.alive, got, tc.want)
		}
	}
}
