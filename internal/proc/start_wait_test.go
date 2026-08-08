package proc

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Start must not return before a process that prints immediately has actually
// written its output. This was a fixed 200ms sleep, which is a bet on machine
// speed: it held on a developer box and lost on a loaded CI runner, where
// TestLogWritingAndTail read an empty log and failed. Start now waits for real
// evidence, so an immediate read is safe regardless of host speed.
func TestStartWaitsForFirstOutput(t *testing.T) {
	dir := t.TempDir()
	ws := t.TempDir()
	info, err := Start(dir, "emitter", "echo first-line; sleep 30", "", ws)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = Stop(dir, "emitter") })

	// No sleep here on purpose: this is the caller pattern that used to race.
	raw, err := os.ReadFile(info.Log)
	if err != nil {
		t.Fatalf("read log immediately after Start: %v", err)
	}
	if !strings.Contains(string(raw), "first-line") {
		t.Fatalf("Start returned before output was visible: %q", string(raw))
	}
}

// A process that prints nothing must not stall Start for the full settle
// window's worth of pointless waiting on every call — servers and sleepers are
// normal, and start latency is paid by every proc_start.
func TestStartDoesNotStallOnSilentProcess(t *testing.T) {
	dir := t.TempDir()
	ws := t.TempDir()
	began := time.Now()
	info, err := Start(dir, "silent", "sleep 30", "", ws)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = Stop(dir, "silent") })
	if elapsed := time.Since(began); elapsed > startSettleTimeout+time.Second {
		t.Fatalf("silent start took %v, want <= %v", elapsed, startSettleTimeout+time.Second)
	}
	if !info.Alive {
		t.Fatal("silent process reported not alive")
	}
}

// A command that fails instantly must not make Start burn the whole settle
// window waiting for output that never comes.
//
// Note this does not assert Alive==false: the child is spawned with
// Process.Release(), so nobody reaps it and it lingers as a zombie that still
// answers kill(pid, 0). Reporting exited-immediately is the caller's job
// (proc.Start's Alive flag is best-effort here) — what matters for start
// latency is that we return promptly instead of polling a corpse.
func TestStartReturnsQuicklyWhenProcessExits(t *testing.T) {
	dir := t.TempDir()
	ws := t.TempDir()
	began := time.Now()
	if _, err := Start(dir, "doomed", "exit 7", "", ws); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = Stop(dir, "doomed") })
	if elapsed := time.Since(began); elapsed > startSettleTimeout+time.Second {
		t.Fatalf("exiting process took %v to report", elapsed)
	}
}

// The custom log name path must honour the same guarantee.
func TestStartWaitsForFirstOutputCustomLog(t *testing.T) {
	dir := t.TempDir()
	ws := t.TempDir()
	info, err := Start(dir, "custom", "echo hello-custom; sleep 30", "custom.log", ws)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = Stop(dir, "custom") })
	if filepath.Base(info.Log) != "custom.log" {
		t.Fatalf("log = %q, want custom.log", info.Log)
	}
	raw, err := os.ReadFile(info.Log)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "hello-custom") {
		t.Fatalf("custom log empty at Start return: %q", string(raw))
	}
}
