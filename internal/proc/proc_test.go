package proc

import (
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
	// Give it a moment to write + exit.
	time.Sleep(150 * time.Millisecond)
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
	time.Sleep(150 * time.Millisecond)
	if st, _ := Status(dir, "server"); st.Alive {
		t.Fatal("server should be dead after stop")
	}
	// Already-running guard.
	a, _ := Start(dir, "dup", "sleep 30", "", "")
	if _, err := Start(dir, "dup", "sleep 30", "", ""); err != ErrAlreadyRunning {
		t.Fatalf("want ErrAlreadyRunning, got %v", err)
	}
	_, _ = Stop(dir, "dup")
	_ = a
}
