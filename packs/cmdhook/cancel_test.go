package cmdhook

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"
)

// captureLogs swaps the default slog handler for the duration of fn and
// returns everything written.
func captureLogs(t *testing.T, fn func()) string {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	defer slog.SetDefault(prev)
	fn()
	return buf.String()
}

// Ctrl+C cancels the whole context. Every in-flight hook then dies with
// "context canceled", and warning about each one is noise at exactly the
// moment the user asked to stop. In the TUI (which keeps Warn+ on stderr) it
// also paints over the alt-screen and corrupts the display.
func TestCancelIsSilent(t *testing.T) {
	root := t.TempDir()
	slow := scriptAt(t, root, "slow.sh", `sleep 5`)
	writeHooksJSON(t, root, oneEntry("", slow))
	b := mustLoad(t, PluginConfig{Root: root, TimeoutSec: 30})

	ctx, cancel := context.WithCancel(context.Background())
	var out outcome
	start := time.Now()
	logs := captureLogs(t, func() {
		go func() {
			time.Sleep(50 * time.Millisecond)
			cancel()
		}()
		out = b.run(ctx, "PreToolUse", "Bash", map[string]any{})
	})
	elapsed := time.Since(start)

	// Cancel must return promptly. exec.CommandContext only signals the direct
	// `sh`; without a process-group kill, grandchildren keep the inherited
	// stdout/stderr pipes open and cmd.Run blocks for the hook's full runtime
	// (5s here) — Ctrl+C would appear to hang.
	if elapsed > 2*time.Second {
		t.Errorf("cancel took %v; expected prompt return (process group not killed?)", elapsed)
	}

	if out.blocked {
		t.Errorf("a cancelled hook must not block: %+v", out)
	}
	if strings.Contains(logs, "context canceled") {
		t.Errorf("cancel must not be logged as a hook failure:\n%s", logs)
	}
	if strings.Contains(logs, "hook failed") {
		t.Errorf("cancel is not a failure:\n%s", logs)
	}
}

// An already-dead context must not spawn subprocesses at all. Stop hooks fire
// during teardown, where ctx is usually already cancelled.
func TestAlreadyCancelledSkipsExec(t *testing.T) {
	root := t.TempDir()
	// Writes a marker file if it ever runs.
	marker := root + "/ran.txt"
	sh := scriptAt(t, root, "mark.sh", `echo ran > `+marker)
	writeHooksJSON(t, root, oneEntry("", sh))
	b := mustLoad(t, PluginConfig{Root: root, TimeoutSec: 30})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	logs := captureLogs(t, func() {
		if out := b.run(ctx, "PreToolUse", "Bash", map[string]any{}); out.blocked {
			t.Errorf("cancelled run must not block: %+v", out)
		}
	})
	if logs != "" {
		t.Errorf("cancelled run must be silent, got:\n%s", logs)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Error("hook subprocess ran despite a cancelled context")
	}
}

// A hook that blows its own budget is a real problem and must still be
// reported — the cancel suppression must not swallow timeouts.
func TestTimeoutIsStillReported(t *testing.T) {
	root := t.TempDir()
	slow := scriptAt(t, root, "slow.sh", `sleep 5`)
	writeHooksJSON(t, root, oneEntry("", slow))
	b := mustLoad(t, PluginConfig{Root: root, TimeoutSec: 1})

	logs := captureLogs(t, func() {
		if out := b.run(context.Background(), "PreToolUse", "Bash", map[string]any{}); out.blocked {
			t.Errorf("timed-out hook must be non-blocking: %+v", out)
		}
	})
	if !strings.Contains(logs, "timed out") {
		t.Errorf("a hook exceeding its own timeout must be reported:\n%s", logs)
	}
}

// An ordinary failing hook still warns: only cancellation is suppressed.
func TestOrdinaryFailureStillWarns(t *testing.T) {
	root := t.TempDir()
	bad := scriptAt(t, root, "bad.sh", `echo boom >&2; exit 1`)
	writeHooksJSON(t, root, oneEntry("", bad))
	b := mustLoad(t, PluginConfig{Root: root, TimeoutSec: 30})

	logs := captureLogs(t, func() {
		b.run(context.Background(), "PreToolUse", "Bash", map[string]any{})
	})
	if !strings.Contains(logs, "hook failed") {
		t.Errorf("a genuinely failing hook must still warn:\n%s", logs)
	}
	if !strings.Contains(logs, "boom") {
		t.Errorf("stderr should be included for a real failure:\n%s", logs)
	}
}
