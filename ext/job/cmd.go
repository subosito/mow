package job

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

  mow job [--schedules path] [engine flags]
  mow job run|serve [--schedules path] [engine flags]   # same as default

Schedules YAML (default $MOW_HOME/job/schedules.yaml) or extensions.job:

  schedules:
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

func cmdRun(args []string) int {
	fs := cliutil.NewFlagSet("job")
	var ef cliutil.EngineFlags
	ef.Bind(fs)
	schedPath := fs.String("schedules", "", "schedules yaml path (default $MOW_HOME/job/schedules.yaml)")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	var jobs []Job
	var err error
	path := strings.TrimSpace(*schedPath)
	if path == "" {
		path = DefaultSchedulesPath()
	}
	if _, statErr := os.Stat(path); statErr == nil {
		jobs, err = LoadSchedules(path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "mow job: load schedules: %v\n", err)
			return 1
		}
	} else {
		eng, eerr := ef.NewEngine()
		if eerr != nil {
			fmt.Fprintf(os.Stderr, "mow job: %v (or create %s)\n", eerr, DefaultSchedulesPath())
			return 1
		}
		jobs, err = LoadSchedulesFromEngine(eng)
		if err != nil {
			fmt.Fprintf(os.Stderr, "mow job: extension config: %v\n", err)
			return 1
		}
	}
	if len(jobs) == 0 {
		fmt.Fprintln(os.Stderr, "mow job: no schedules configured")
		return 1
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	d := &Daemon{
		Schedules: jobs,
		NewEngine: func() (*mow.Engine, error) {
			return ef.NewEngine()
		},
	}
	fmt.Fprintf(os.Stderr, "job: %d schedule(s); ctrl+c to stop\n", len(jobs))
	if err := d.Start(ctx); err != nil && ctx.Err() == nil {
		fmt.Fprintf(os.Stderr, "mow job: %v\n", err)
		return 1
	}
	return 0
}
