package engine

import (
	"testing"
	"time"
)

func TestRetryGateSuppressesWaitCopyDuringBackoff(t *testing.T) {
	base := time.Now()
	g := &retryGate{now: func() time.Time { return base }}

	// A new call's elapsed-0 tick clears any prior call's marker.
	if copy, suppress := g.waitCopy(0); suppress || copy == "" {
		t.Fatalf("elapsed-0 tick = %q,%v want copy, no suppress", copy, suppress)
	}

	// Retry scheduled: 2s backoff sleep starts now.
	g.note(2 * time.Second)

	// A tick landing inside the sleep is suppressed: the gateway is not being
	// asked, so "silent" copy would be a lie (the retry copy stays on screen).
	if copy, suppress := g.waitCopy(10 * time.Second); !suppress || copy != "" {
		t.Fatalf("mid-backoff tick = %q,%v want suppressed", copy, suppress)
	}

	// After the sleep, silence is measured from the retry's end —
	// approximately the new attempt's start — not from the original request.
	g.now = func() time.Time { return base.Add(2600 * time.Millisecond) }
	copy, suppress := g.waitCopy(12 * time.Second)
	if suppress {
		t.Fatal("post-backoff tick must not be suppressed")
	}
	if copy != "waiting for first response" {
		t.Fatalf("post-backoff copy %q must return to neutral waiting copy", copy)
	}

	// The next call's elapsed-0 tick clears the marker again.
	if _, suppress := g.waitCopy(0); suppress {
		t.Fatal("a new call must clear the gate")
	}
	if copy, _ := g.waitCopy(5 * time.Second); copy != "waiting for first response" {
		t.Fatalf("after clearing, copy %q must stay neutral", copy)
	}
}

func TestRetryCopyHonestPerKind(t *testing.T) {
	cases := []struct {
		name string
		info RetryInfo
		want string
	}{
		{"busy with status", RetryInfo{Attempt: 1, Delay: 3 * time.Second, Status: 429, Kind: RetryBusy}, "provider busy · retrying in 3s"},
		{"busy without status", RetryInfo{Attempt: 1, Delay: 2 * time.Second, Kind: RetryBusy}, "provider busy · retrying in 2s"},
		{"unavailable", RetryInfo{Attempt: 1, Delay: 5 * time.Second, Kind: RetryUnavailable}, "provider unavailable · reconnecting in 5s"},
		{"network", RetryInfo{Attempt: 1, Delay: time.Second, Kind: RetryNetwork}, "network error · retrying in 1s"},
		{"sub-second delay rounds up", RetryInfo{Attempt: 1, Delay: 200 * time.Millisecond, Kind: RetryBusy}, "provider busy · retrying in 1s"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := retryCopy(tc.info); got != tc.want {
				t.Fatalf("retryCopy(%+v) = %q, want %q", tc.info, got, tc.want)
			}
		})
	}
}
