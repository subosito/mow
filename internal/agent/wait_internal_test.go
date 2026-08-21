package agent

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/subosito/mow/internal/llm"
)

// fastMonitor builds a monitor on a millisecond schedule so tests never wait
// on the real 10s/30s thresholds.
func fastMonitor(onWait func(time.Duration), onActive func()) *modelWaitMonitor {
	m := newModelWaitMonitor(onWait, onActive)
	m.schedule = []time.Duration{5 * time.Millisecond}
	m.interval = 10 * time.Millisecond
	return m
}

func TestModelWaitMonitorTicksUntilSignal(t *testing.T) {
	var mu sync.Mutex
	var waits []time.Duration
	active := 0
	m := fastMonitor(func(el time.Duration) {
		mu.Lock()
		waits = append(waits, el)
		mu.Unlock()
	}, func() {
		mu.Lock()
		active++
		mu.Unlock()
	})
	m.begin(context.Background())
	// The immediate wait fired synchronously in begin, before any tick.
	mu.Lock()
	if len(waits) != 1 || waits[0] != 0 {
		mu.Unlock()
		t.Fatalf("immediate wait=%v, want exactly one at elapsed 0", waits)
	}
	mu.Unlock()
	// Let a couple of threshold ticks land, then signal first-byte activity.
	time.Sleep(25 * time.Millisecond)
	m.signal()
	select {
	case <-m.doneCh:
	case <-time.After(time.Second):
		t.Fatal("ticker goroutine did not stop after signal")
	}
	m.signal() // idempotent: must not re-fire active
	mu.Lock()
	defer mu.Unlock()
	if active != 1 {
		t.Fatalf("active fired %d times, want exactly 1", active)
	}
	if len(waits) < 2 {
		t.Fatalf("waits=%v, want immediate + at least one threshold tick", waits)
	}
	for _, w := range waits[1:] {
		if w <= 0 {
			t.Fatalf("threshold tick elapsed=%v, want > 0", w)
		}
	}
}

func TestModelWaitMonitorStopsOnContextCancel(t *testing.T) {
	var mu sync.Mutex
	waits := 0
	m := fastMonitor(func(time.Duration) {
		mu.Lock()
		waits++
		mu.Unlock()
	}, nil)
	ctx, cancel := context.WithCancel(context.Background())
	m.begin(ctx)
	cancel()
	select {
	case <-m.doneCh:
	case <-time.After(time.Second):
		t.Fatal("ticker goroutine leaked after ctx cancel")
	}
	m.stop() // chat-return path after cancel: no panic, no further ticks
	mu.Lock()
	defer mu.Unlock()
	if waits != 1 {
		t.Fatalf("waits=%d, want only the immediate tick", waits)
	}
}

// Run-level: the immediate wait fires before chat is entered, active fires
// once when the call returns, and no threshold tick lands on the real
// 10s/30s schedule for a call held open only briefly.
func TestRunModelWaitAndActive(t *testing.T) {
	var mu sync.Mutex
	var waits []time.Duration
	active := 0
	inChat := make(chan struct{})
	release := make(chan struct{})
	chat := func(ctx context.Context, _ []llm.Message, _ []llm.ToolSpec) (llm.Message, error) {
		close(inChat)
		<-release
		return llm.Message{Role: "assistant", Content: "ok"}, nil
	}
	done := make(chan error, 1)
	go func() {
		_, err := Run(context.Background(), chat, "hi", Options{
			OnModelWait: func(el time.Duration) {
				mu.Lock()
				waits = append(waits, el)
				mu.Unlock()
			},
			OnModelActive: func() {
				mu.Lock()
				active++
				mu.Unlock()
			},
		})
		done <- err
	}()
	<-inChat
	mu.Lock()
	if len(waits) != 1 || waits[0] != 0 {
		mu.Unlock()
		t.Fatalf("waits=%v, want one immediate wait at elapsed 0", waits)
	}
	mu.Unlock()
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if active != 1 {
		t.Fatalf("active=%d, want 1 on chat return", active)
	}
	if len(waits) != 1 {
		t.Fatalf("waits=%v, want no threshold ticks on the real schedule", waits)
	}
}
