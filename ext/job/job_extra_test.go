package job

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/subosito/mow"
	"github.com/subosito/mow/ext/goal"
)

func TestParseCronErrorsAndEdgeCases(t *testing.T) {
	t.Parallel()

	invalidExprs := []string{
		"",
		"* * * *",       // 4 fields
		"* * * * * *",   // 6 fields
		"60 * * * *",    // min out of range
		"* 24 * * *",    // hour out of range
		"* * 0 * *",     // dom out of range
		"* * 32 * *",    // dom out of range
		"* * * 0 *",     // month out of range
		"* * * 13 *",    // month out of range
		"* * * * 8",     // dow out of range
		"*/0 * * * *",   // zero step
		"*/abc * * * *", // bad step
		"10-5 * * * *",  // range min > max
		"abc * * * *",   // non-integer
	}

	for _, expr := range invalidExprs {
		if _, err := parseCron(expr); err == nil {
			t.Errorf("parseCron(%q) expected error, got nil", expr)
		}
	}

	// Valid complex cron expressions
	validExprs := []struct {
		expr string
		test func(*cronSched)
	}{
		{
			expr: "0 9 * * 7", // Sunday=7 normalized to 0
			test: func(c *cronSched) {
				if !c.dow.vals[0] || c.dow.vals[7] {
					t.Errorf("expected dow 0 set and 7 removed, got %+v", c.dow.vals)
				}
			},
		},
		{
			expr: "*/15 9-17 1,15 1-6 1-5",
			test: func(c *cronSched) {
				if !c.min.vals[0] || !c.min.vals[45] {
					t.Errorf("min step parsing failed")
				}
				if !c.hour.vals[9] || !c.hour.vals[17] {
					t.Errorf("hour range parsing failed")
				}
			},
		},
	}

	for _, tt := range validExprs {
		c, err := parseCron(tt.expr)
		if err != nil {
			t.Errorf("parseCron(%q) unexpected error: %v", tt.expr, err)
			continue
		}
		tt.test(c)
	}
}

func TestCronDOMAndDOWLogic(t *testing.T) {
	t.Parallel()

	// Cron with both DOM and DOW specified (OR logic per POSIX cron standard)
	// Match if 1st of month OR Monday (dow=1)
	c, err := parseCron("0 0 1 * 1")
	if err != nil {
		t.Fatal(err)
	}

	// 2026-06-01 is Monday (both DOM=1 and DOW=1 match)
	t1 := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	if !c.match(t1) {
		t.Errorf("expected match on 2026-06-01")
	}

	// 2026-06-08 is Monday (DOM=8, DOW=1) -> match via DOW
	t2 := time.Date(2026, 6, 8, 0, 0, 0, 0, time.UTC)
	if !c.match(t2) {
		t.Errorf("expected match on 2026-06-08 via DOW")
	}

	// 2026-07-01 is Wednesday (DOM=1, DOW=3) -> match via DOM
	t3 := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	if !c.match(t3) {
		t.Errorf("expected match on 2026-07-01 via DOM")
	}

	// 2026-06-02 is Tuesday (DOM=2, DOW=2) -> no match
	t4 := time.Date(2026, 6, 2, 0, 0, 0, 0, time.UTC)
	if c.match(t4) {
		t.Errorf("did not expect match on 2026-06-02")
	}
}

func TestCronDSTTransitions(t *testing.T) {
	t.Parallel()

	// Spring forward transition: e.g. America/New_York 2026-03-08 02:00 -> 03:00
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skip("America/New_York timezone not available")
	}

	// Schedule for 02:30 AM (which is skipped on spring forward)
	c, err := parseCron("30 2 8 3 *")
	if err != nil {
		t.Fatal(err)
	}

	beforeGap := time.Date(2026, 3, 8, 1, 59, 0, 0, loc)
	next, err := c.nextAfter(beforeGap)
	if err != nil {
		t.Fatalf("nextAfter during spring forward failed: %v", err)
	}
	// Spring forward gap fires right after transition (03:00)
	if next.Hour() != 3 || next.Minute() != 0 {
		t.Errorf("expected spring-forward gap jump to 03:00, got %s", next.Format(time.RFC3339))
	}
}

func TestLoadSchedulesAndValidateJob(t *testing.T) {
	t.Parallel()

	t.Run("ValidateJob errors", func(t *testing.T) {
		t.Parallel()
		disabled := false

		tests := []struct {
			job     Job
			wantErr string
		}{
			{Job{Prompt: "hi"}, "missing id"},
			{Job{ID: "j1"}, "need goal or prompt"},
			{Job{ID: "j2", Prompt: "hi", Enabled: &disabled}, "disabled"},
			{Job{ID: "j3", Prompt: "hi", Cron: "invalid cron"}, "cron"},
			{Job{ID: "j4", Prompt: "hi"}, "need every or cron"},
			{Job{ID: "j5", Prompt: "hi", Every: "invalid"}, "bad every"},
			{Job{ID: "j6", Prompt: "hi", Every: "-5m"}, "bad every"},
		}

		for _, tt := range tests {
			err := ValidateJob(tt.job)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("ValidateJob(%+v) err = %v, wantErr %q", tt.job, err, tt.wantErr)
			}
		}
	})

	t.Run("LoadSchedules file reading", func(t *testing.T) {
		t.Parallel()
		tmpDir := t.TempDir()
		path := filepath.Join(tmpDir, "schedules.yaml")

		yamlData := `
schedules:
  - id: j1
    every: 1h
    prompt: hello
`
		if err := os.WriteFile(path, []byte(yamlData), 0600); err != nil {
			t.Fatal(err)
		}

		jobs, err := LoadSchedules(path)
		if err != nil {
			t.Fatalf("LoadSchedules failed: %v", err)
		}
		if len(jobs) != 1 || jobs[0].ID != "j1" {
			t.Fatalf("unexpected jobs: %+v", jobs)
		}

		// Non-existent path
		if _, err := LoadSchedules(filepath.Join(tmpDir, "nope.yaml")); err == nil {
			t.Fatal("expected error for non-existent path")
		}
	})

	t.Run("LoadSchedulesFromEngine", func(t *testing.T) {
		t.Parallel()
		if _, err := LoadSchedulesFromEngine(nil); err == nil {
			t.Fatal("expected error for nil engine")
		}
	})
}

func TestNextFireAndTruncateLog(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	jEvery := Job{Every: "10m"}
	if nf := NextFire(jEvery, now); !strings.Contains(nf, "every 10m") {
		t.Errorf("NextFire every got %q", nf)
	}

	jCron := Job{Cron: "0 13 * * *"}
	if nf := NextFire(jCron, now); !strings.Contains(nf, "2026-01-01T13:00:00Z") {
		t.Errorf("NextFire cron got %q", nf)
	}

	jInvalid := Job{Cron: "invalid"}
	if nf := NextFire(jInvalid, now); nf != "invalid cron" {
		t.Errorf("NextFire invalid cron got %q", nf)
	}

	// Truncate log
	if s := truncateLog("hello", 10); s != "hello" {
		t.Errorf("truncateLog small got %q", s)
	}
	if s := truncateLog("hello world", 5); !strings.Contains(s, "truncated") {
		t.Errorf("truncateLog truncated got %q", s)
	}
}

func TestDaemonStartAndFiring(t *testing.T) {
	t.Parallel()

	t.Run("Start error checking", func(t *testing.T) {
		t.Parallel()
		var d *Daemon
		if err := d.Start(context.Background()); err == nil {
			t.Fatal("expected error for nil daemon")
		}

		d = &Daemon{Schedules: []Job{}}
		if err := d.Start(context.Background()); err == nil {
			t.Fatal("expected error for empty schedules")
		}

		disabled := false
		d = &Daemon{
			NewEngine: func() (*mow.Engine, error) { return nil, nil },
			Schedules: []Job{{ID: "d1", Every: "1h", Prompt: "p", Enabled: &disabled}},
		}
		if err := d.Start(context.Background()); err == nil {
			t.Fatal("expected error for no runnable schedules")
		}
	})

	t.Run("fire with prompt success", func(t *testing.T) {
		t.Parallel()

		var logs []string
		d := &Daemon{
			Schedules: []Job{{ID: "j1", Every: "100ms", Prompt: "hello"}},
			OnLog: func(msg string) {
				logs = append(logs, msg)
			},
			NewEngine: func() (*mow.Engine, error) {
				return mow.New(mow.Options{
					NoSession: true,
					Chat: func(ctx context.Context, messages []mow.Message, tools []mow.ToolSpec) (mow.Message, error) {
						return mow.Message{Role: "assistant", Content: "job response text"}, nil
					},
				})
			},
		}

		// Run every loop directly for 1 tick
		d.fire(context.Background(), d.Schedules[0])

		hasTick := false
		for _, l := range logs {
			if strings.Contains(l, "job response text") {
				hasTick = true
				break
			}
		}
		if !hasTick {
			t.Fatalf("expected log output with job response text, got logs: %v", logs)
		}
	})

	t.Run("fire with goal re-run", func(t *testing.T) {
		t.Parallel()

		storeDir := t.TempDir()
		store := &goal.Store{Dir: storeDir}

		// Create a completed goal
		g := goal.State{
			ID:     "goal-1",
			Goal:   "do task",
			Status: goal.StatusDone,
		}
		if err := store.Save(g); err != nil {
			t.Fatal(err)
		}

		var logs []string
		d := &Daemon{
			GoalStore: store,
			NewEngine: func() (*mow.Engine, error) {
				return mow.New(mow.Options{
					NoSession: true,
					Chat: func(ctx context.Context, messages []mow.Message, tools []mow.ToolSpec) (mow.Message, error) {
						return mow.Message{Role: "assistant", Content: "done goal"}, nil
					},
				})
			},
			OnLog: func(msg string) {
				logs = append(logs, msg)
			},
		}

		d.fire(context.Background(), Job{ID: "j-goal", Goal: "goal-1"})

		reloaded, err := store.Load("goal-1")
		if err != nil {
			t.Fatal(err)
		}
		if reloaded.Status == goal.StatusPending {
			t.Fatalf("expected goal status to change from pending, logs: %v", logs)
		}
	})

	t.Run("Daemon Start loop execution", func(t *testing.T) {
		t.Parallel()

		d := &Daemon{
			Schedules: []Job{
				{ID: "j-every", Every: "10ms", Prompt: "prompt1"},
				{ID: "j-cron", Cron: "* * * * *", Prompt: "prompt2"},
			},
			NewEngine: func() (*mow.Engine, error) {
				return mow.New(mow.Options{
					NoSession: true,
					Chat: func(ctx context.Context, messages []mow.Message, tools []mow.ToolSpec) (mow.Message, error) {
						return mow.Message{Role: "assistant", Content: "ok"}, nil
					},
				})
			},
			OnLog: func(msg string) {},
		}

		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()

		_ = d.Start(ctx)
	})
}

func TestJobCLICommands(t *testing.T) {
	t.Parallel()

	for _, flag := range []string{"-h", "--help", "help"} {
		if code := runCmd([]string{flag}); code != 0 {
			t.Errorf("runCmd(%q) = %d; want 0", flag, code)
		}
	}

	if code := runCmd([]string{"unknown-verb"}); code != 2 {
		t.Errorf("runCmd(unknown-verb) = %d; want 2", code)
	}

	if code := runCmd([]string{"run", "--every", "invalid"}); code != 2 {
		t.Errorf("runCmd(invalid inline) = %d; want 2", code)
	}

	if code := runCmd([]string{"check"}); code != 1 {
		t.Errorf("runCmd(check no schedules) = %d; want 1", code)
	}

	if code := runCmd([]string{"list"}); code != 0 {
		t.Errorf("runCmd(list no schedules) = %d; want 0", code)
	}

	t.Run("cmdCheck and cmdList with valid schedules file", func(t *testing.T) {
		t.Parallel()

		tmpDir := t.TempDir()
		schedFile := filepath.Join(tmpDir, "schedules.yaml")
		yamlContent := `
schedules:
  - id: job-1
    every: 5m
    prompt: "check status"
`
		if err := os.WriteFile(schedFile, []byte(yamlContent), 0600); err != nil {
			t.Fatal(err)
		}

		if code := cmdCheck([]string{"--schedules", schedFile}); code != 0 {
			t.Errorf("cmdCheck valid file = %d; want 0", code)
		}

		if code := cmdList([]string{"--schedules", schedFile}); code != 0 {
			t.Errorf("cmdList valid file = %d; want 0", code)
		}
	})

	t.Run("InlineJob constructor", func(t *testing.T) {
		t.Parallel()

		j, err := InlineJob("custom-id", "10m", "", "", "do prompt")
		if err != nil || j.ID != "custom-id" || j.Every != "10m" {
			t.Fatalf("InlineJob failed: %v, %+v", err, j)
		}

		if _, err := InlineJob("", "", "", "", ""); err == nil {
			t.Fatal("InlineJob expected error for empty parameters")
		}
	})
}
