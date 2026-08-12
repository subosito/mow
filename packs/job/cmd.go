package job

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/subosito/mow"
	"github.com/subosito/mow/cliutil"
	"github.com/subosito/mow/ext"
)

func init() {
	ext.RegisterCommand(ext.Command{
		Name:    "job",
		Summary: "Interval jobs — run | list | check",
		Run:     runCmd,
	})
}

func runCmd(args []string) int {
	// Default action: run the daemon (short CLI surface).
	if len(args) == 0 {
		return cmdRun(nil)
	}
	switch args[0] {
	case "serve", "run":
		return cmdRun(args[1:])
	case "list", "ls":
		return cmdList(args[1:])
	case "check", "validate":
		return cmdCheck(args[1:])
	case "help", "-h", "--help":
		printUsage()
		return 0
	default:
		// Flags for the daemon, e.g. mow job --schedules path
		if strings.HasPrefix(args[0], "-") {
			return cmdRun(args)
		}
		fmt.Fprintf(os.Stderr, "mow job: unknown %q\n", args[0])
		printUsage()
		return 2
	}
}

func printUsage() {
	fmt.Fprintf(os.Stderr, `mow job — interval / cron jobs (in-process)

Commands:

  mow job run|serve [flags]     start daemon (default if no verb)
  mow job list                  list schedules
  mow job check                 validate schedules

Quick (no schedule file):

  mow job --every 10m --prompt "Summarize git status" [engine flags]
  mow job --every 1h --goal fix-ci --allow-write --allow-shell
  mow job --cron "0 9 * * 1-5" --prompt "Morning brief"

Flags:

  --every 10m     interval (first tick immediate)
  --cron "…"      5-field min hour dom month dow (local)
  --prompt TEXT   prompt each tick
  --goal ID       re-run saved goal each tick
  --id NAME       job id (default: inline)
  --schedules path   yaml file (default $MOW_HOME/job/schedules.yaml)
                     explicit path must exist (no silent fallback)

File / extensions.job:

  schedules:
    - id: hourly
      every: 1h
      goal: fix-ci

Overlapping ticks for the same id are skipped. Ctrl+C stops.
For fleet ops prefer: mow ops run NAME

`)
}

func loadSchedulesForCLI(schedPath string, ef *cliutil.EngineFlags) ([]Job, string, error) {
	explicit := strings.TrimSpace(schedPath) != ""
	path := strings.TrimSpace(schedPath)
	if path == "" {
		path = DefaultSchedulesPath()
	}
	_, statErr := os.Lstat(path)
	if statErr == nil {
		jobs, err := LoadSchedules(path)
		return jobs, path, err
	}
	if !os.IsNotExist(statErr) {
		return nil, path, statErr
	}
	if explicit {
		return nil, path, fmt.Errorf("no file at %s", path)
	}
	if ef == nil {
		return nil, path, fmt.Errorf("no file at %s (and no engine flags to load extensions.job)", path)
	}
	eng, err := ef.NewEngine()
	if err != nil {
		return nil, path, fmt.Errorf("%v (or create %s)", err, DefaultSchedulesPath())
	}
	defer eng.Close()
	jobs, err := LoadSchedulesFromEngine(eng)
	return jobs, "extensions.job", err
}

func cmdList(args []string) int {
	fs := cliutil.NewFlagSet("job list")
	var ef cliutil.EngineFlags
	ef.Bind(fs)
	schedPath := fs.String("schedules", "", "schedules yaml path")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	jobs, src, err := loadSchedulesForCLI(*schedPath, &ef)
	if err != nil {
		fmt.Fprintf(os.Stderr, "mow job list: %v\n", err)
		return 1
	}
	if len(jobs) == 0 {
		fmt.Println("(no schedules)")
		fmt.Fprintf(os.Stderr, "source: %s\n", src)
		return 0
	}
	now := time.Now()
	tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tWHEN\tTARGET\tNEXT\tOK")
	for _, j := range jobs {
		when := strings.TrimSpace(j.Every)
		if c := strings.TrimSpace(j.Cron); c != "" {
			when = "cron " + c
		}
		target := strings.TrimSpace(j.Goal)
		if target != "" {
			target = "goal:" + target
		} else {
			target = "prompt"
		}
		ok := "yes"
		if err := ValidateJob(j); err != nil {
			ok = err.Error()
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", j.ID, when, target, NextFire(j, now), ok)
	}
	_ = tw.Flush()
	fmt.Fprintf(os.Stderr, "source: %s (%d)\n", src, len(jobs))
	return 0
}

func cmdCheck(args []string) int {
	fs := cliutil.NewFlagSet("job check")
	var ef cliutil.EngineFlags
	ef.Bind(fs)
	schedPath := fs.String("schedules", "", "schedules yaml path")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	jobs, src, err := loadSchedulesForCLI(*schedPath, &ef)
	if err != nil {
		fmt.Fprintf(os.Stderr, "mow job check: %v\n", err)
		return 1
	}
	if len(jobs) == 0 {
		fmt.Fprintln(os.Stderr, "mow job check: no schedules configured")
		return 1
	}
	bad := 0
	for _, j := range jobs {
		if err := ValidateJob(j); err != nil {
			if errors.Is(err, ErrDisabled) {
				fmt.Printf("disabled %s\n", j.ID)
				continue
			}
			fmt.Fprintf(os.Stderr, "bad %s: %v\n", j.ID, err)
			bad++
			continue
		}
		fmt.Printf("ok %s next=%s\n", j.ID, NextFire(j, time.Now()))
	}
	if err := duplicateScheduleIDs(jobs); err != nil {
		fmt.Fprintf(os.Stderr, "mow job check: %v\n", err)
		bad++
	}
	fmt.Fprintf(os.Stderr, "source: %s checked=%d bad=%d\n", src, len(jobs), bad)
	if bad > 0 {
		return 1
	}
	return 0
}

func cmdRun(args []string) int {
	fs := cliutil.NewFlagSet("job")
	var ef cliutil.EngineFlags
	ef.Bind(fs)
	schedPath := fs.String("schedules", "", "schedules yaml path (default $MOW_HOME/job/schedules.yaml)")
	// Inline schedule (goal-style): no file required.
	every := fs.String("every", "", "inline interval, e.g. 10m (no schedules file)")
	cronExpr := fs.String("cron", "", "inline 5-field cron (local)")
	id := fs.String("id", "", "inline job id (default: inline)")
	goalID := fs.String("goal", "", "inline: saved goal id to run each tick")
	prompt := fs.String("prompt", "", "inline: prompt text each tick")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	var jobs []Job
	var src string
	if inlineJobRequested(*every, *cronExpr, *goalID, *prompt) {
		j, err := InlineJob(*id, *every, *cronExpr, *goalID, *prompt)
		if err != nil {
			fmt.Fprintf(os.Stderr, "mow job: %v\n", err)
			return 2
		}
		jobs = []Job{j}
		src = "flags"
	} else {
		var err error
		jobs, src, err = loadSchedulesForCLI(*schedPath, &ef)
		if err != nil {
			fmt.Fprintf(os.Stderr, "mow job: %v\n", err)
			fmt.Fprintln(os.Stderr, "hint: mow job --every 10m --prompt \"…\"  (no file needed)")
			return 1
		}
		if len(jobs) == 0 {
			fmt.Fprintln(os.Stderr, "mow job: no schedules configured")
			fmt.Fprintln(os.Stderr, "hint: mow job --every 10m --prompt \"…\"  or create $MOW_HOME/job/schedules.yaml")
			return 1
		}
	}
	// Refuse to start with invalid schedules.
	for _, j := range jobs {
		if err := ValidateJob(j); err != nil && !errors.Is(err, ErrDisabled) {
			fmt.Fprintf(os.Stderr, "mow job: schedule %q: %v (mow job check)\n", j.ID, err)
			return 1
		}
	}
	if err := duplicateScheduleIDs(jobs); err != nil {
		fmt.Fprintf(os.Stderr, "mow job: %v\n", err)
		return 1
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	d := &Daemon{
		Schedules: jobs,
		NewEngine: func() (*mow.Engine, error) {
			return ef.NewEngineCLI()
		},
	}
	fmt.Fprintf(os.Stderr, "job: %d schedule(s) from %s; ctrl+c to stop\n", len(jobs), src)
	if err := d.Start(ctx); err != nil && ctx.Err() == nil {
		fmt.Fprintf(os.Stderr, "mow job: %v\n", err)
		return 1
	}
	return 0
}

// inlineJobRequested is true when the user passed any inline schedule flag.
func inlineJobRequested(every, cron, goalID, prompt string) bool {
	return strings.TrimSpace(every) != "" ||
		strings.TrimSpace(cron) != "" ||
		strings.TrimSpace(goalID) != "" ||
		strings.TrimSpace(prompt) != ""
}

// InlineJob builds a single Job from CLI-style fields (no schedules file).
func InlineJob(id, every, cron, goalID, prompt string) (Job, error) {
	j := Job{
		ID:     strings.TrimSpace(id),
		Every:  strings.TrimSpace(every),
		Cron:   strings.TrimSpace(cron),
		Goal:   strings.TrimSpace(goalID),
		Prompt: strings.TrimSpace(prompt),
	}
	if j.ID == "" {
		j.ID = "inline"
	}
	if j.Every == "" && j.Cron == "" {
		return Job{}, fmt.Errorf("need --every (e.g. 10m) or --cron")
	}
	if j.Goal == "" && j.Prompt == "" {
		return Job{}, fmt.Errorf("need --goal or --prompt")
	}
	if err := ValidateJob(j); err != nil {
		return Job{}, err
	}
	return j, nil
}
