package acp

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"sync"
	"testing"
	"time"
)

// Prompt cancel must send session/cancel so the peer can stop (not only
// abandon the wait and leave the peer working until timeout).
func TestPromptCancelSendsSessionCancel(t *testing.T) {
	peerIn, clientOut := io.Pipe()
	clientIn, peerOut := io.Pipe()
	c := &Client{
		stdin:   clientOut,
		stdout:  clientIn,
		pending: map[string]chan response{},
		started: true,
		exited:  make(chan struct{}),
	}
	go c.readLoop()

	var mu sync.Mutex
	var sawCancel bool
	var promptID json.RawMessage
	go func() {
		enc := json.NewEncoder(peerOut)
		sc := bufio.NewScanner(peerIn)
		sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
		for sc.Scan() {
			var probe map[string]json.RawMessage
			if json.Unmarshal(sc.Bytes(), &probe) != nil {
				continue
			}
			var method string
			_ = json.Unmarshal(probe["method"], &method)
			if method == "session/cancel" {
				mu.Lock()
				sawCancel = true
				mu.Unlock()
				// After cancel, finish the in-flight prompt so call's grace path
				// can drain (optional — test mainly checks cancel was sent).
				if promptID != nil {
					_ = enc.Encode(map[string]any{
						"jsonrpc": "2.0", "id": jsonRaw(promptID),
						"result": map[string]any{"stopReason": "cancelled"},
					})
				}
				continue
			}
			if method != "session/prompt" {
				continue
			}
			promptID = probe["id"]
			// Hang until cancel — never answer promptly.
			// (cancel path waits cancelGrace then returns)
		}
	}()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, _, _, err := c.Prompt(ctx, "sess-1", "do a long thing")
		done <- err
	}()
	// Let the prompt request leave the client.
	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err != context.Canceled {
			t.Fatalf("err=%v want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Prompt did not return after cancel")
	}

	// Allow the peer goroutine to observe session/cancel.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		ok := sawCancel
		mu.Unlock()
		if ok {
			_ = clientOut.Close()
			_ = peerOut.Close()
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("session/cancel never sent to peer")
}

func jsonRaw(id json.RawMessage) any {
	var v any
	if json.Unmarshal(id, &v) == nil {
		return v
	}
	return string(id)
}
