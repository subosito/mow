package acp

import (
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
