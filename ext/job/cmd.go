package job

import (
	"context"
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
		Summary: "Run interval jobs (goals or prompts) until stopped",
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
	case "list":
		return cmdList(args[1:])
	case "check":
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
	fmt.Fprintf(os.Stderr, `mow job — in-process interval jobs

  mow job [--schedules path] [engine flags]           run daemon until Ctrl+C
  mow job run|serve [--schedules path] [engine flags] same as default
  mow job list  [--schedules path]                    list schedules
  mow job check [--schedules path]                    validate schedules (exit 1 if any bad)

Schedules YAML (default $MOW_HOME/job/schedules.yaml) or extensions.job:

  schedules:
    - id: hourly-ci
      every: 1h                 # Go duration; first tick runs immediately
      goal: fix-ci
    - id: morning
      cron: "0 9 * * 1-5"       # 5-field min hour dom month dow (local)
      prompt: "Summarize git status"

Cron fields: min hour dom month dow (local). Prefer host cron for HA.
Overlapping ticks for the same id are skipped (one fire at a time).
Each tick logs "result:" with the prompt reply or goal summary (stderr).

`)
}

func loadSchedulesForCLI(schedPath string, ef *cliutil.EngineFlags) ([]Job, string, error) {
	path := strings.TrimSpace(schedPath)
	if path == "" {
		path = DefaultSchedulesPath()
	}
	if _, statErr := os.Stat(path); statErr == nil {
		jobs, err := LoadSchedules(path)
		return jobs, path, err
	}
	if ef == nil {
		return nil, path, fmt.Errorf("no file at %s (and no engine flags to load extensions.job)", path)
	}
	eng, err := ef.NewEngine()
	if err != nil {
		return nil, path, fmt.Errorf("%v (or create %s)", err, DefaultSchedulesPath())
	}
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
			fmt.Fprintf(os.Stderr, "bad %s: %v\n", j.ID, err)
			bad++
			continue
		}
		fmt.Printf("ok %s next=%s\n", j.ID, NextFire(j, time.Now()))
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
	if err := fs.Parse(args); err != nil {
		return 2
	}

	jobs, src, err := loadSchedulesForCLI(*schedPath, &ef)
	if err != nil {
		fmt.Fprintf(os.Stderr, "mow job: %v\n", err)
		return 1
	}
	if len(jobs) == 0 {
		fmt.Fprintln(os.Stderr, "mow job: no schedules configured")
		return 1
	}
	// Refuse to start with invalid schedules.
	for _, j := range jobs {
		if err := ValidateJob(j); err != nil && err.Error() != "disabled" {
			fmt.Fprintf(os.Stderr, "mow job: schedule %q: %v (mow job check)\n", j.ID, err)
			return 1
		}
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
