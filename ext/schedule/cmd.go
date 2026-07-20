package schedule

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/subosito/mow"
	"github.com/subosito/mow/cliutil"
	"github.com/subosito/mow/ext"
)

func init() {
	ext.RegisterCommand(ext.Command{
		Name:    "schedule",
		Summary: "Run interval jobs (goals or prompts) until stopped",
		Run:     runCmd,
	})
}

func runCmd(args []string) int {
	if len(args) == 0 {
		printUsage()
		return 2
	}
	switch args[0] {
	case "serve", "run":
		return cmdServe(args[1:])
	case "help", "-h", "--help":
		printUsage()
		return 0
	default:
		fmt.Fprintf(os.Stderr, "mow schedule: unknown %q\n", args[0])
		printUsage()
		return 2
	}
}

func printUsage() {
	fmt.Fprintf(os.Stderr, `mow schedule — in-process interval jobs

  serve [--jobs path] [engine flags]

Jobs YAML (default $MOW_HOME/schedule/jobs.yaml) or extensions.schedule:

  jobs:
    - id: hourly-ci
      every: 1h                 # Go duration; first tick runs immediately
      goal: fix-ci
    - id: morning
      cron: "0 9 * * 1-5"       # 5-field min hour dom month dow (local)
      prompt: "Summarize git status"

Cron fields: min hour dom month dow (local). Prefer host cron for HA.
Each tick logs "result:" with the prompt reply or goal summary (stderr).

`)
}

func cmdServe(args []string) int {
	fs := cliutil.NewFlagSet("schedule serve")
	var ef cliutil.EngineFlags
	ef.Bind(fs)
	jobsPath := fs.String("jobs", "", "jobs yaml path (default $MOW_HOME/schedule/jobs.yaml)")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	var jobs []Job
	var err error
	path := strings.TrimSpace(*jobsPath)
	if path == "" {
		path = DefaultJobsPath()
	}
	if _, statErr := os.Stat(path); statErr == nil {
		jobs, err = LoadJobs(path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "mow schedule: load jobs: %v\n", err)
			return 1
		}
	} else {
		eng, eerr := ef.NewEngine()
		if eerr != nil {
			fmt.Fprintf(os.Stderr, "mow schedule: %v (or create %s)\n", eerr, DefaultJobsPath())
			return 1
		}
		jobs, err = LoadJobsFromEngine(eng)
		if err != nil {
			fmt.Fprintf(os.Stderr, "mow schedule: extension config: %v\n", err)
			return 1
		}
	}
	if len(jobs) == 0 {
		fmt.Fprintln(os.Stderr, "mow schedule: no jobs configured")
		return 1
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	d := &Daemon{
		Jobs: jobs,
		NewEngine: func() (*mow.Engine, error) {
			return ef.NewEngine()
		},
	}
	fmt.Fprintf(os.Stderr, "schedule: %d job(s); ctrl+c to stop\n", len(jobs))
	if err := d.Start(ctx); err != nil && ctx.Err() == nil {
		fmt.Fprintf(os.Stderr, "mow schedule: %v\n", err)
		return 1
	}
	return 0
}
