// Package job runs interval or cron jobs that invoke goals or one-shot prompts.
//
//	import _ "github.com/subosito/mow/packs/job"
//
// Ways to schedule:
//   - Inline CLI (no file): mow job --every 10m --prompt "…" [engine flags]
//   - File: $MOW_HOME/job/schedules.yaml
//   - Config: extensions.job.schedules
package job

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/subosito/mow"
	"github.com/subosito/mow/packs/goal"
)

const (
	maxSchedules          = 64
	maxSchedulesFileBytes = 1 << 20 // 1 MiB
	minEvery              = time.Second
)

// ErrDisabled is returned by ValidateJob when the schedule is present but off.
var ErrDisabled = errors.New("disabled")

// Job is one recurring unit of work (an entry under schedules).
type Job struct {
	ID string `yaml:"id"`
	// Every is a Go duration string, e.g. "1h", "30m".
	Every string `yaml:"every"`
	// Cron is a 5-field expression: min hour dom month dow (local time).
	// Use either Every or Cron (Cron wins if both set).
	Cron string `yaml:"cron"`
	// Goal is a saved goal id (mow goal) to run.
	Goal string `yaml:"goal"`
	// Prompt is a one-shot user prompt (ignored if Goal set).
	Prompt string `yaml:"prompt"`
	// Enabled defaults true when omitted.
	Enabled *bool `yaml:"enabled"`
}

// Config is extensions.job (list key is schedules, not jobs).
type Config struct {
	Schedules []Job `yaml:"schedules"`
}

// DefaultSchedulesPath is $MOW_HOME/job/schedules.yaml.
func DefaultSchedulesPath() string {
	return filepath.Join(mow.Home(), "job", "schedules.yaml")
}

// LoadSchedules reads YAML from path (or DefaultSchedulesPath).
// The path must be a regular file (a symlink to a regular file is allowed)
// and is capped at 1 MiB / 64 schedules.
func LoadSchedules(path string) ([]Job, error) {
	if strings.TrimSpace(path) == "" {
		path = DefaultSchedulesPath()
	}
	raw, err := readSchedulesFile(path)
	if err != nil {
		return nil, err
	}
	var c Config
	if err := yaml.Unmarshal(raw, &c); err != nil {
		return nil, err
	}
	return capSchedules(c.Schedules)
}

// LoadSchedulesFromEngine reads extensions.job from the engine config.
func LoadSchedulesFromEngine(eng *mow.Engine) ([]Job, error) {
	if eng == nil {
		return nil, fmt.Errorf("job: nil engine")
	}
	var c Config
	if err := eng.Extension("job", &c); err != nil {
		return nil, err
	}
	return capSchedules(c.Schedules)
}

func capSchedules(jobs []Job) ([]Job, error) {
	if len(jobs) > maxSchedules {
		return nil, fmt.Errorf("job: more than %d schedules", maxSchedules)
	}
	return jobs, nil
}

func readSchedulesFile(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		info, err = os.Stat(path)
		if err != nil {
			return nil, err
		}
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("job: schedules path is not a regular file")
	}
	if info.Size() > maxSchedulesFileBytes {
		return nil, fmt.Errorf("job: schedules file exceeds %d bytes", maxSchedulesFileBytes)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxSchedulesFileBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxSchedulesFileBytes {
		return nil, fmt.Errorf("job: schedules file exceeds %d bytes", maxSchedulesFileBytes)
	}
	return data, nil
}

// ValidateJob checks one schedule is runnable (id, every/cron, goal/prompt).
func ValidateJob(j Job) error {
	id := strings.TrimSpace(j.ID)
	if id == "" {
		return fmt.Errorf("missing id")
	}
	if strings.ContainsAny(id, "\x00\n\r\t") {
		return fmt.Errorf("id contains invalid characters")
	}
	if strings.TrimSpace(j.Goal) == "" && strings.TrimSpace(j.Prompt) == "" {
		return fmt.Errorf("need goal or prompt")
	}
	if j.Enabled != nil && !*j.Enabled {
		return ErrDisabled
	}
	cronExpr := strings.TrimSpace(j.Cron)
	everyExpr := strings.TrimSpace(j.Every)
	if cronExpr != "" {
		if _, err := parseCron(cronExpr); err != nil {
			return err
		}
		return nil
	}
	if everyExpr == "" {
		return fmt.Errorf("need every or cron")
	}
	dur, err := time.ParseDuration(everyExpr)
	if err != nil || dur <= 0 {
		return fmt.Errorf("bad every %q", j.Every)
	}
	return nil
}

// NextFire returns a human next-fire hint (cron next time, or every interval).
func NextFire(j Job, from time.Time) string {
	if cronExpr := strings.TrimSpace(j.Cron); cronExpr != "" {
		sched, err := parseCron(cronExpr)
		if err != nil {
			return "invalid cron"
		}
		next, err := sched.nextAfter(from)
		if err != nil {
			return err.Error()
		}
		return next.Format(time.RFC3339)
	}
	if every := strings.TrimSpace(j.Every); every != "" {
		return "every " + every + " (first tick immediate when daemon starts)"
	}
	return ""
}

func duplicateScheduleIDs(jobs []Job) error {
	seen := make(map[string]struct{}, len(jobs))
	for _, j := range jobs {
		id := strings.TrimSpace(j.ID)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			return fmt.Errorf("job: duplicate id %q", id)
		}
		seen[id] = struct{}{}
	}
	return nil
}

// Daemon runs schedules until ctx is cancelled.
type Daemon struct {
	// NewEngine must return a fresh Engine the daemon owns. Each tick
	// Closes that value when the tick ends (including on error or cancel).
	NewEngine func() (*mow.Engine, error)
	Schedules []Job
	OnLog     func(string)
	GoalStore *goal.Store

	// fireMu serializes fires per job id so a slow tick cannot overlap the next.
	fireMu sync.Map // id -> *sync.Mutex
	logMu  sync.Mutex
}

// Start blocks until ctx done.
func (d *Daemon) Start(ctx context.Context) error {
	if d == nil || d.NewEngine == nil {
		return fmt.Errorf("job: NewEngine required")
	}
	jobs := d.Schedules
	if len(jobs) == 0 {
		return fmt.Errorf("job: no schedules")
	}
	if len(jobs) > maxSchedules {
		return fmt.Errorf("job: more than %d schedules", maxSchedules)
	}
	if err := duplicateScheduleIDs(jobs); err != nil {
		return err
	}
	var wg sync.WaitGroup
	started := 0
	for _, j := range jobs {
		j.ID = strings.TrimSpace(j.ID)
		if j.ID == "" {
			d.log("skip job: missing id")
			continue
		}
		if j.Enabled != nil && !*j.Enabled {
			continue
		}
		if strings.TrimSpace(j.Goal) == "" && strings.TrimSpace(j.Prompt) == "" {
			d.log(fmt.Sprintf("skip job %q: need goal or prompt", j.ID))
			continue
		}
		cronExpr := strings.TrimSpace(j.Cron)
		everyExpr := strings.TrimSpace(j.Every)
		if cronExpr != "" {
			sched, err := parseCron(cronExpr)
			if err != nil {
				d.log(fmt.Sprintf("skip job %q: %v", j.ID, err))
				continue
			}
			started++
			wg.Go(func() {
				d.runCronLoop(ctx, j, sched)
			})
			continue
		}
		if everyExpr == "" {
			d.log(fmt.Sprintf("skip job %q: need every or cron", j.ID))
			continue
		}
		dur, err := time.ParseDuration(everyExpr)
		if err != nil || dur <= 0 {
			d.log(fmt.Sprintf("skip job %q: bad every %q", j.ID, j.Every))
			continue
		}
		if dur < minEvery {
			d.log(fmt.Sprintf("job %s: every %s raised to %s", j.ID, dur, minEvery))
			dur = minEvery
		}
		started++
		wg.Go(func() {
			d.runEveryLoop(ctx, j, dur)
		})
	}
	if started == 0 {
		return fmt.Errorf("job: no runnable schedules")
	}
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return err
	}
	return fmt.Errorf("job: all schedules stopped")
}

func (d *Daemon) runEveryLoop(ctx context.Context, j Job, every time.Duration) {
	if ctx.Err() != nil {
		return
	}
	// Fire once at start so short demos (e.g. every: 30s) are not silent for a full interval.
	d.fire(ctx, j)
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			d.fire(ctx, j)
		}
	}
}

func (d *Daemon) runCronLoop(ctx context.Context, j Job, sched *cronSched) {
	for {
		next, err := sched.nextAfter(time.Now())
		if err != nil {
			d.log(fmt.Sprintf("job %s cron: %v", j.ID, err))
			return
		}
		wait := time.Until(next)
		if wait < 0 {
			wait = 0
		}
		d.log(fmt.Sprintf("job %s next at %s", j.ID, next.Format(time.RFC3339)))
		t := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C:
			d.fire(ctx, j)
		}
	}
}

func (d *Daemon) fire(ctx context.Context, j Job) {
	if ctx.Err() != nil {
		return
	}
	// One in-flight fire per schedule id (skip if still running).
	muAny, _ := d.fireMu.LoadOrStore(j.ID, &sync.Mutex{})
	mu := muAny.(*sync.Mutex)
	if !mu.TryLock() {
		st := recordTickSkip(j.ID, "previous tick still running")
		d.log(fmt.Sprintf("job %s skip: previous tick still running (consecutive=%d total=%d)", j.ID, st.SkipCount, st.SkipTotal))
		return
	}
	defer mu.Unlock()

	recordTickStart(j.ID)
	status := "error"
	var tickErr string
	defer func() {
		recordTickEnd(j.ID, status, tickErr)
	}()

	d.log(fmt.Sprintf("job %s tick", j.ID))
	eng, err := d.NewEngine()
	if err != nil {
		tickErr = err.Error()
		d.log(fmt.Sprintf("job %s engine: %v", j.ID, err))
		return
	}
	defer eng.Close()
	if ctx.Err() != nil {
		return
	}
	if g := strings.TrimSpace(j.Goal); g != "" {
		store := d.goalStore()
		if prev, err := store.Load(g); err == nil {
			switch prev.Status {
			case goal.StatusDone:
				// Store.Reset also restores plan items; a hand-rolled
				// Status=pending write left checklists marked done.
				if _, err := store.Reset(g); err != nil {
					d.log(fmt.Sprintf("job %s goal %s reset: %v", j.ID, g, err))
					return
				}
				d.log(fmt.Sprintf("job %s goal %s reset for re-run", j.ID, g))
			case goal.StatusBlocked:
				status = "blocked"
				d.log(fmt.Sprintf("job %s skip: goal %s is blocked (resume with --answer)", j.ID, g))
				return
			}
		} else if !os.IsNotExist(err) {
			tickErr = err.Error()
			d.log(fmt.Sprintf("job %s goal %s: %v", j.ID, g, err))
			return
		}
		r := &goal.Runner{Engine: eng, Store: store}
		st, err := r.Run(ctx, g)
		if err != nil {
			tickErr = err.Error()
			d.log(fmt.Sprintf("job %s goal %s: %v status=%s", j.ID, g, err, st.Status))
			return
		}
		status = "ok"
		sum := strings.TrimSpace(st.Summary)
		if sum == "" {
			sum = strings.TrimSpace(st.LastReply)
		}
		d.log(fmt.Sprintf("job %s goal %s status=%s", j.ID, g, st.Status))
		if sum != "" {
			d.log(fmt.Sprintf("job %s result:\n%s", j.ID, truncateLog(sum, jobResultLogMax)))
		}
		return
	}
	res, err := eng.Prompt(ctx, j.Prompt)
	if err != nil {
		tickErr = err.Error()
		d.log(fmt.Sprintf("job %s prompt: %v", j.ID, err))
		return
	}
	status = "ok"
	d.log(fmt.Sprintf("job %s prompt ok session=%s", j.ID, res.SessionID))
	if t := strings.TrimSpace(res.Text); t != "" {
		d.log(fmt.Sprintf("job %s result:\n%s", j.ID, truncateLog(t, jobResultLogMax)))
	}
}

// jobResultLogMax is the max runes of a tick result written to the job log
// (stderr / OnLog). 800 was far too small for ops summaries; 32k still
// bounds runaway model output for journald.
const jobResultLogMax = 32_000

func truncateLog(s string, n int) string {
	s = strings.TrimSpace(s)
	if n <= 0 {
		return s
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + fmt.Sprintf("…\n(truncated, %d chars total)", len(r))
}

func (d *Daemon) goalStore() *goal.Store {
	if d.GoalStore != nil {
		return d.GoalStore
	}
	return &goal.Store{}
}

func (d *Daemon) log(s string) {
	d.logMu.Lock()
	defer d.logMu.Unlock()
	if d.OnLog != nil {
		d.OnLog(s)
		return
	}
	fmt.Fprintln(os.Stderr, "job:", s)
}
