package tty

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/subosito/mow"
)

func TestLoopConfigurationBoundsAndLifecycle(t *testing.T) {
	var l ttyLoop
	if got := l.configure(t.Context(), nil, []string{"0m", "x"}, func(string) {}); !strings.Contains(got, "duration") {
		t.Fatal(got)
	}
	got := l.configure(t.Context(), nil, []string{"1m", "--max", "2", "--for", "2m", "--ephemeral", "check", "ci"}, func(string) {})
	if !strings.Contains(got, "scheduled") || l.schedule == nil || l.schedule.prompt != "check ci" || !l.schedule.ephemeral || l.schedule.max != 2 {
		t.Fatalf("%s %+v", got, l.schedule)
	}
	if got := l.configure(t.Context(), nil, []string{"status"}, func(string) {}); !strings.Contains(got, "0/2") {
		t.Fatal(got)
	}
	if got := l.configure(t.Context(), nil, []string{"stop"}, func(string) {}); got != "loop stopped" || l.schedule != nil {
		t.Fatal(got)
	}
}
func TestLoopFireSkipsBusyWithoutQueuing(t *testing.T) {
	var l ttyLoop
	l.promptMu.Lock()
	defer l.promptMu.Unlock()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	s := &ttySchedule{prompt: "x", interval: time.Minute, next: time.Now(), expires: time.Now().Add(time.Hour), max: 2, cancel: cancel}
	l.schedule = s
	l.fire(ctx, (*mow.Engine)(nil), s, func(string) {}, false)
	if s.skipped != 1 || s.fires != 0 {
		t.Fatalf("%+v", s)
	}
}
