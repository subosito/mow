package proc

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestStartStatusTailStop(t *testing.T) {
	dir := t.TempDir()

	// A short-lived process writes to its log and exits.
	info, err := Start(dir, "hello", "echo hi from proc", "", "")
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if info.PID <= 0 {
		t.Fatalf("bad pid: %+v", info)
	}
	// Start already waits for first output, so the tail is readable now.
	if out, _ := Tail(dir, "hello", 10); !strings.Contains(out, "hi from proc") {
		t.Fatalf("tail missing output: %q", out)
	}

	// A long-lived process stays alive until stopped.
	sv, err := Start(dir, "server", "sleep 30", "", "")
	if err != nil {
		t.Fatalf("start server: %v", err)
	}
	if !sv.Alive {
		t.Fatal("server should be alive")
	}
	st, err := Status(dir, "server")
	if err != nil || !st.Alive {
		t.Fatalf("status: %+v err=%v", st, err)
	}
	list, _ := List(dir)
	if len(list) != 2 {
		t.Fatalf("want 2 procs, got %d", len(list))
	}
	if _, err := Stop(dir, "server"); err != nil {
		t.Fatalf("stop: %v", err)
	}
	// SIGTERM delivery and reaping are asynchronous: poll rather than betting a
	// fixed sleep is long enough (that bet is what made CI flaky).
	deadline := time.Now().Add(3 * time.Second)
	for {
		st, _ := Status(dir, "server")
		if !st.Alive {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("server should be dead after stop")
		}
		time.Sleep(5 * time.Millisecond)
	}
	// Already-running guard.
	a, _ := Start(dir, "dup", "sleep 30", "", "")
	if _, err := Start(dir, "dup", "sleep 30", "", ""); err != ErrAlreadyRunning {
		t.Fatalf("want ErrAlreadyRunning, got %v", err)
	}
	_, _ = Stop(dir, "dup")
	_ = a
}

func TestStopTreatsRecycledPIDAsStale(t *testing.T) {
	// The test binary itself is alive but (normally) not a session leader —
	// exactly what a recycled pid looks like. Stop must not signal it.
	self := os.Getpid()
	if ownsSession(self) {
		t.Skip("test process is a session leader; cannot model a recycled pid")
	}
	dir := t.TempDir()
	if err := writePID(dir, "stale", self); err != nil {
		t.Fatal(err)
	}
	info, err := Stop(dir, "stale")
	if err != nil {
		t.Fatalf("stop: %v", err)
	}
	if info.Alive {
		t.Fatalf("stale pid must report not running: %+v", info)
	}
	if _, err := os.Stat(filepath.Join(dir, "stale.pid")); !os.IsNotExist(err) {
		t.Fatalf("stale pid file must be removed, stat err=%v", err)
	}
}

func TestStartScrubsCredentialEnv(t *testing.T) {
	t.Setenv("MOW_API_KEY", "super-secret")
	t.Setenv("SOME_VENDOR_API_KEY", "also-secret")
	t.Setenv("PLAIN_VAR", "visible")
	dir := t.TempDir()
	if _, err := Start(dir, "env", "env", "", ""); err != nil {
		t.Fatalf("start: %v", err)
	}
	deadline := time.Now().Add(3 * time.Second)
	var out string
	for {
		out, _ = Tail(dir, "env", 0)
		if strings.Contains(out, "PLAIN_VAR=visible") || time.Now().After(deadline) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !strings.Contains(out, "PLAIN_VAR=visible") {
		t.Fatalf("ordinary env must reach the child: %q", out)
	}
	if strings.Contains(out, "super-secret") || strings.Contains(out, "also-secret") {
		t.Fatalf("credentials leaked into child env: %q", out)
	}
}
