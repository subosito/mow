package acp

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

// A real short-lived peer exercises Cmd.Wait/stdout ordering, not just
// response-channel scheduling. The test binary is a hermetic helper process.
func TestClientFinalReplyOnProcessExit(t *testing.T) {
	if os.Getenv("MOW_TEST_ACP_EXIT_PEER") == "1" {
		sc := bufio.NewScanner(os.Stdin)
		if sc.Scan() {
			var req request
			_ = json.Unmarshal(sc.Bytes(), &req)
			_ = json.NewEncoder(os.Stdout).Encode(response{JSONRPC: "2.0", ID: req.ID, Result: "pong"})
		}
		os.Exit(0)
	}
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	c := &Client{Command: []string{binary, "-test.run=^TestClientFinalReplyOnProcessExit$"}, Env: []string{"MOW_TEST_ACP_EXIT_PEER=1", "GORACE=atexit_sleep_ms=0"}}
	defer c.Close()
	for range 10 {
		if err := c.startProcess(); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
		got, err := c.call(ctx, "ping", nil)
		cancel()
		if err != nil || string(got) != `"pong"` {
			t.Fatalf("final reply: %s, %v", got, err)
		}
		if err := c.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestClientCloseUnblocksPipeWriter(t *testing.T) {
	c := &Client{Command: []string{"sleep", "60"}}
	if err := c.startProcess(); err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	cmd := c.cmd
	writing := make(chan struct{})
	written := make(chan error, 1)
	go func() {
		c.encMu.Lock()
		defer c.encMu.Unlock()
		close(writing)
		_, err := c.stdin.Write([]byte(strings.Repeat("x", 1<<20)))
		written <- err
	}()
	<-writing
	closed := make(chan error, 1)
	go func() { closed <- c.Close() }()
	select {
	case err := <-closed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		// Avoid leaving the pre-fix writer parked after test failure.
		killPeerTreeHard(cmd)
		t.Fatal("Close waited for encoder lock before stopping the peer")
	}
	select {
	case err := <-written:
		if err == nil {
			t.Fatal("write unexpectedly succeeded against non-reading peer")
		}
	case <-time.After(time.Second):
		t.Fatal("pipe writer still blocked after Close")
	}
}
