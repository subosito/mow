package acp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestPeerIdleDuration(t *testing.T) {
	if d := peerIdleDuration(0); d != 15*time.Minute {
		t.Fatalf("default=%v", d)
	}
	if d := peerIdleDuration(-1); d != 0 {
		t.Fatalf("disabled=%v", d)
	}
	if d := peerIdleDuration(60); d != time.Minute {
		t.Fatalf("custom=%v", d)
	}
}

func TestEvictIdlePeers(t *testing.T) {
	// Minimal fake: slots with lastUsed only; client nil path is cleaned.
	tool := &delegateTool{
		peerIdle: 10 * time.Millisecond,
		peers: map[string]*peerSlot{
			"a\x00/tmp": {lastUsed: time.Now().Add(-time.Second)},
			"b\x00/tmp": {lastUsed: time.Now()},
		},
	}
	tool.evictIdleLocked(time.Now())
	if _, ok := tool.peers["a\x00/tmp"]; ok {
		t.Fatal("idle peer should be evicted")
	}
	// nil client entries are deleted; b has nil client so also gone
	if len(tool.peers) != 0 {
		t.Fatalf("peers=%v", tool.peers)
	}
}

// fakePeerClient wires a Client to an in-process peer (no subprocess) that
// echoes each session/prompt back as one agent_message_chunk.
func fakePeerClient() (*Client, func()) {
	peerIn, clientOut := io.Pipe() // client stdin → peer
	clientIn, peerOut := io.Pipe() // peer → client stdout
	c := &Client{
		stdin:   clientOut,
		stdout:  clientIn,
		pending: map[string]chan response{},
		started: true,
		exited:  make(chan struct{}),
	}
	go c.readLoop()
	go func() {
		enc := json.NewEncoder(peerOut)
		sc := bufio.NewScanner(peerIn)
		sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
		for sc.Scan() {
			var req struct {
				ID     json.RawMessage `json:"id"`
				Method string          `json:"method"`
				Params struct {
					SessionID string         `json:"sessionId"`
					Prompt    []ContentBlock `json:"prompt"`
				} `json:"params"`
			}
			if json.Unmarshal(sc.Bytes(), &req) != nil || req.Method != "session/prompt" {
				continue
			}
			text := ""
			if len(req.Params.Prompt) > 0 {
				text = req.Params.Prompt[0].Text
			}
			_ = enc.Encode(map[string]any{
				"jsonrpc": "2.0", "method": "session/update",
				"params": map[string]any{
					"sessionId": req.Params.SessionID,
					"update": map[string]any{
						"sessionUpdate": "agent_message_chunk",
						"content":       map[string]any{"type": "text", "text": "echo:" + text},
					},
				},
			})
			var id any
			_ = json.Unmarshal(req.ID, &id)
			_ = enc.Encode(map[string]any{
				"jsonrpc": "2.0", "id": id,
				"result": map[string]any{"stopReason": "end_turn"},
			})
		}
	}()
	return c, func() {
		_ = clientOut.Close()
		_ = peerOut.Close()
	}
}

func TestDelegateConcurrentSamePeer(t *testing.T) {
	c, cleanup := fakePeerClient()
	defer cleanup()
	tool := &delegateTool{
		agents: map[string]AgentSpec{
			"fake": {Name: "fake", Command: []string{"unused"}, TimeoutSec: 30},
		},
		peers: map[string]*peerSlot{
			peerKey("fake", ""): {client: c, sessionID: "s1", lastUsed: time.Now()},
		},
	}
	const n = 8
	var wg sync.WaitGroup
	replies := make([]string, n)
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			args, _ := json.Marshal(map[string]string{"agent": "fake", "prompt": fmt.Sprintf("p%d", i)})
			replies[i], errs[i] = tool.Exec(context.Background(), args)
		}(i)
	}
	wg.Wait()
	for i := 0; i < n; i++ {
		if errs[i] != nil {
			t.Fatalf("call %d: %v", i, errs[i])
		}
		want := fmt.Sprintf("echo:p%d", i)
		if !strings.Contains(replies[i], want) {
			t.Fatalf("call %d got %q, want it to contain %q", i, replies[i], want)
		}
	}
}

func TestResolveInWorkspace(t *testing.T) {
	ws := t.TempDir()
	sibling := ws + "-evil"
	if err := os.MkdirAll(sibling, 0o755); err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(sibling)
	if err := os.MkdirAll(filepath.Join(ws, "..foo"), 0o755); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(ws, "esc")); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name    string
		path    string
		wantErr bool
	}{
		{"workspace root", ".", false},
		{"inside relative", "sub/file.txt", false},
		{"inside absolute", filepath.Join(ws, "a.txt"), false},
		{"dir named ..foo relative", "..foo", false},
		{"dir named ..foo absolute", filepath.Join(ws, "..foo"), false},
		{"parent escape", "../x", true},
		{"sibling with shared prefix", sibling, true},
		{"symlink escape", "esc/x.txt", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := resolveInWorkspace(ws, tc.path)
			if (err != nil) != tc.wantErr {
				t.Fatalf("resolveInWorkspace(%q, %q) err=%v, wantErr=%v", ws, tc.path, err, tc.wantErr)
			}
		})
	}
}
