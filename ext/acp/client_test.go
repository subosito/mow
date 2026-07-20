package acp

import (
	"testing"
	"time"
)

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
