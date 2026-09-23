package tty

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/subosito/mow"
)

// ttyLoop uses its own prompts while readline remains exclusively on the main
// goroutine. Engine serializes Prompt, so an interactive turn and scheduled fire
// cannot overlap; a fire skips when TryLock fails instead of queuing debt.
type ttyLoop struct {
	mu       sync.Mutex
	schedule *ttySchedule
	promptMu sync.Mutex
}
type ttySchedule struct {
	prompt              string
	interval            time.Duration
	next, expires       time.Time
	max, fires, skipped int
	ephemeral           bool
	cancel              context.CancelFunc
}

func parseTTYDuration(s string) (time.Duration, error) {
	if len(s) < 2 {
		return 0, fmt.Errorf("duration like 5m required")
	}
	n, err := strconv.Atoi(s[:len(s)-1])
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("duration like 5m required")
	}
	switch s[len(s)-1] {
	case 'm':
		return time.Duration(n) * time.Minute, nil
	case 'h':
		return time.Duration(n) * time.Hour, nil
	}
	return 0, fmt.Errorf("duration unit must be m/h")
}
func (l *ttyLoop) configure(ctx context.Context, eng *mow.Engine, args []string, report func(string)) string {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(args) == 0 {
		return "usage: /loop 5m [--max N] [--for 1h] [--ephemeral] PROMPT | status | now | stop"
	}
	switch args[0] {
	case "stop":
		if l.schedule != nil {
			l.schedule.cancel()
		}
		l.schedule = nil
		return "loop stopped"
	case "status":
		if l.schedule == nil {
			return "loop inactive"
		}
		s := l.schedule
		return fmt.Sprintf("loop every %s · %d/%d fires · %d skipped", s.interval, s.fires, s.max, s.skipped)
	case "now":
		if l.schedule == nil {
			return "loop inactive"
		}
		go l.fire(ctx, eng, l.schedule, report, true)
		return "loop armed now"
	}
	interval, err := parseTTYDuration(args[0])
	if err != nil {
		return err.Error()
	}
	if interval < time.Minute {
		return "loop minimum interval is 1m"
	}
	maxFires := 24
	lifetime := 24 * time.Hour
	ephemeral := false
	i := 1
	for i < len(args) && strings.HasPrefix(args[i], "--") {
		switch args[i] {
		case "--ephemeral":
			ephemeral = true
			i++
		case "--max":
			if i+1 >= len(args) {
				return "missing --max"
			}
			maxFires, _ = strconv.Atoi(args[i+1])
			i += 2
		case "--for":
			if i+1 >= len(args) {
				return "missing --for"
			}
			lifetime, err = parseTTYDuration(args[i+1])
			if err != nil {
				return err.Error()
			}
			i += 2
		default:
			return "invalid /loop option"
		}
	}
	prompt := strings.Join(args[i:], " ")
	if strings.TrimSpace(prompt) == "" || maxFires < 1 || maxFires > 100 || lifetime < interval || lifetime > 7*24*time.Hour {
		return "invalid loop bounds or empty prompt"
	}
	if l.schedule != nil {
		l.schedule.cancel()
	}
	sctx, cancel := context.WithCancel(ctx)
	now := time.Now()
	s := &ttySchedule{prompt: prompt, interval: interval, next: now.Add(interval), expires: now.Add(lifetime), max: maxFires, ephemeral: ephemeral, cancel: cancel}
	l.schedule = s
	go l.run(sctx, eng, s, report)
	return fmt.Sprintf("loop scheduled every %s (max %d)", interval, maxFires)
}
func (l *ttyLoop) run(ctx context.Context, eng *mow.Engine, s *ttySchedule, report func(string)) {
	timer := time.NewTimer(time.Until(s.next))
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-timer.C:
			l.fire(ctx, eng, s, report, false)
			l.mu.Lock()
			if l.schedule != s || s.fires >= s.max || now.After(s.expires) {
				if l.schedule == s {
					l.schedule = nil
				}
				l.mu.Unlock()
				return
			}
			s.next = time.Now().Add(s.interval)
			next := s.next
			l.mu.Unlock()
			timer.Reset(time.Until(next))
		}
	}
}
func (l *ttyLoop) fire(ctx context.Context, eng *mow.Engine, s *ttySchedule, report func(string), manual bool) {
	if !l.promptMu.TryLock() {
		l.mu.Lock()
		if l.schedule == s {
			s.skipped++
		}
		l.mu.Unlock()
		report("loop fire skipped (busy)")
		return
	}
	defer l.promptMu.Unlock()
	l.mu.Lock()
	if l.schedule != s || (!manual && (time.Now().After(s.expires) || s.fires >= s.max)) {
		l.mu.Unlock()
		return
	}
	s.fires++
	n, max, prompt, ephemeral := s.fires, s.max, s.prompt, s.ephemeral
	l.mu.Unlock()
	res, err := eng.PromptWith(ctx, prompt, mow.PromptOpts{Ephemeral: ephemeral})
	if err != nil {
		report(fmt.Sprintf("loop %d/%d failed: %v", n, max, err))
		return
	}
	report(fmt.Sprintf("loop %d/%d: %s", n, max, res.Text))
}
func (l *ttyLoop) interactiveLock()   { l.promptMu.Lock() }
func (l *ttyLoop) interactiveUnlock() { l.promptMu.Unlock() }
