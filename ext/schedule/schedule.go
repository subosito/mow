// Package schedule runs interval or cron jobs that invoke goals or one-shot prompts.
//
//	import _ "github.com/subosito/mow/ext/schedule"
//
// Config: extensions.schedule or $MOW_HOME/schedule/jobs.yaml.
package schedule

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/subosito/mow"
	"github.com/subosito/mow/ext/goal"
)

// Job is one recurring unit of work.
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

// Config is extensions.schedule.
type Config struct {
	Jobs []Job `yaml:"jobs"`
}

// DefaultJobsPath is $MOW_HOME/schedule/jobs.yaml.
func DefaultJobsPath() string {
	return filepath.Join(mow.Home(), "schedule", "jobs.yaml")
}

// LoadJobs reads YAML from path (or DefaultJobsPath).
func LoadJobs(path string) ([]Job, error) {
	if strings.TrimSpace(path) == "" {
		path = DefaultJobsPath()
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var c Config
	if err := yaml.Unmarshal(raw, &c); err != nil {
		return nil, err
	}
	return c.Jobs, nil
}

// LoadJobsFromEngine reads extensions.schedule from the engine config.
func LoadJobsFromEngine(eng *mow.Engine) ([]Job, error) {
	if eng == nil {
		return nil, fmt.Errorf("schedule: nil engine")
	}
	var c Config
	if err := eng.Extension("schedule", &c); err != nil {
		return nil, err
	}
	return c.Jobs, nil
}

// Daemon runs jobs until ctx is cancelled.
type Daemon struct {
	NewEngine func() (*mow.Engine, error)
	Jobs      []Job
	OnLog     func(string)
	GoalStore *goal.Store
}

// Start blocks until ctx done.
func (d *Daemon) Start(ctx context.Context) error {
	if d == nil || d.NewEngine == nil {
		return fmt.Errorf("schedule: NewEngine required")
	}
	jobs := d.Jobs
	if len(jobs) == 0 {
		return fmt.Errorf("schedule: no jobs")
	}
	var wg sync.WaitGroup
	started := 0
	for _, j := range jobs {
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
			wg.Add(1)
			go func(j Job, sched *cronSched) {
				defer wg.Done()
				d.runCronLoop(ctx, j, sched)
			}(j, sched)
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
		started++
		wg.Add(1)
		go func(j Job, every time.Duration) {
			defer wg.Done()
			d.runEveryLoop(ctx, j, every)
		}(j, dur)
	}
	if started == 0 {
		return fmt.Errorf("schedule: no runnable jobs")
	}
	wg.Wait()
	return ctx.Err()
}

func (d *Daemon) runEveryLoop(ctx context.Context, j Job, every time.Duration) {
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
	d.log(fmt.Sprintf("job %s tick", j.ID))
	eng, err := d.NewEngine()
	if err != nil {
		d.log(fmt.Sprintf("job %s engine: %v", j.ID, err))
		return
	}
	if g := strings.TrimSpace(j.Goal); g != "" {
		store := d.goalStore()
		// Recurring schedules should re-run completed goals each tick.
		if prev, err := store.Load(g); err == nil && prev.Status == goal.StatusDone {
			prev.Status = goal.StatusPending
			prev.Step = 0
			prev.Error = ""
			prev.SessionID = ""
			prev.LastReply = ""
			// keep Summary as last successful result until overwritten
			if err := store.Save(prev); err != nil {
				d.log(fmt.Sprintf("job %s goal %s reset: %v", j.ID, g, err))
				return
			}
			d.log(fmt.Sprintf("job %s goal %s reset for re-run", j.ID, g))
		}
		r := &goal.Runner{Engine: eng, Store: store}
		st, err := r.Run(ctx, g)
		if err != nil {
			d.log(fmt.Sprintf("job %s goal %s: %v status=%s", j.ID, g, err, st.Status))
			return
		}
		sum := strings.TrimSpace(st.Summary)
		if sum == "" {
			sum = strings.TrimSpace(st.LastReply)
		}
		d.log(fmt.Sprintf("job %s goal %s status=%s", j.ID, g, st.Status))
		if sum != "" {
			d.log(fmt.Sprintf("job %s result:\n%s", j.ID, truncateLog(sum, 800)))
		}
		return
	}
	res, err := eng.Prompt(ctx, j.Prompt)
	if err != nil {
		d.log(fmt.Sprintf("job %s prompt: %v", j.ID, err))
		return
	}
	d.log(fmt.Sprintf("job %s prompt ok session=%s", j.ID, res.SessionID))
	if t := strings.TrimSpace(res.Text); t != "" {
		d.log(fmt.Sprintf("job %s result:\n%s", j.ID, truncateLog(t, 800)))
	}
}

func truncateLog(s string, n int) string {
	s = strings.TrimSpace(s)
	if n <= 0 || len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func (d *Daemon) goalStore() *goal.Store {
	if d.GoalStore != nil {
		return d.GoalStore
	}
	return &goal.Store{}
}

func (d *Daemon) log(s string) {
	if d.OnLog != nil {
		d.OnLog(s)
		return
	}
	fmt.Fprintln(os.Stderr, "schedule:", s)
}
