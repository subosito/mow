package acp

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestClientCloseKillsSleepingPeer(t *testing.T) {
	c := &Client{Command: []string{"sleep", "60"}}
	if err := c.startProcess(); err != nil {
		t.Fatalf("startProcess: %v", err)
	}
	if !c.Alive() {
		t.Fatal("expected peer alive after start")
	}
	if err := c.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for c.Alive() {
		if time.Now().After(deadline) {
			t.Fatal("Alive() still true after Close")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestAliveFlipsAfterExit(t *testing.T) {
	c := &Client{Command: []string{"true"}}
	if err := c.startProcess(); err != nil {
		t.Fatalf("startProcess: %v", err)
	}
	defer func() { _ = c.Close() }()
	deadline := time.Now().Add(5 * time.Second)
	for c.Alive() {
		if time.Now().After(deadline) {
			t.Fatal("Alive() still true after peer process exit")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestCallFailsWhenPeerExits(t *testing.T) {
	// sleep briefly so Start can race the reaper, then exit with no JSON-RPC.
	c := &Client{Command: []string{"sh", "-c", "sleep 0.05"}}
	if err := c.startProcess(); err != nil {
		t.Fatalf("startProcess: %v", err)
	}
	defer func() { _ = c.Close() }()

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	start := time.Now()
	_, err := c.call(ctx, "session/prompt", map[string]any{"sessionId": "x", "prompt": []any{}})
	if err == nil {
		t.Fatal("expected error when peer exits")
	}
	if !strings.Contains(err.Error(), "peer process exited") {
		t.Fatalf("err=%v", err)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatalf("call hung too long after peer exit: %v", time.Since(start))
	}
}

func TestStartFailsWhenPeerNeverSpeaksACP(t *testing.T) {
	c := &Client{Command: []string{"sleep", "60"}}
	defer func() { _ = c.Close() }()

	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	start := time.Now()
	_, err := c.Start(ctx)
	if err == nil {
		t.Fatal("expected handshake error when peer never speaks ACP")
	}
	if time.Since(start) > 3*time.Second {
		t.Fatalf("Start hung too long: %v", time.Since(start))
	}
	if c.Alive() {
		t.Fatal("dead handshake must close the peer")
	}
}
