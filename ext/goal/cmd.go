package goal

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"text/tabwriter"

	"github.com/subosito/mow/cliutil"
	"github.com/subosito/mow/ext"
)

func init() {
	ext.RegisterCommand(ext.Command{
		Name:    "goal",
		Summary: "Outer-loop goals (multi-step Prompt; headless)",
		Run:     runCmd,
	})
}

func runCmd(args []string) int {
	if len(args) == 0 {
		printGoalUsage()
		return 2
	}
	switch args[0] {
	case "new":
		return cmdNew(args[1:])
	case "run":
		return cmdRun(args[1:])
	case "status":
		return cmdStatus(args[1:])
	case "list":
		return cmdList(args[1:])
	case "delete":
		return cmdDelete(args[1:])
	case "reset":
		return cmdReset(args[1:])
	case "help", "-h", "--help":
		printGoalUsage()
		return 0
	default:
		fmt.Fprintf(os.Stderr, "mow goal: unknown subcommand %q\n", args[0])
		printGoalUsage()
		return 2
	}
}

func printGoalUsage() {
	fmt.Fprintf(os.Stderr, `mow goal — multi-step goals over Engine.Prompt

Subcommands:
  new    --id NAME --goal "..." [--max-steps N]   create pending goal
  run    --id NAME | --goal "..." [engine flags]  run until done/fail/max
  status --id NAME                               show saved state
  list                                           list goals under $MOW_HOME/goals
  reset  --id NAME                               clear progress (pending); re-run with run --id
  delete --id NAME                               remove goal file

Engine flags (run): same as other packs (--config --model --workspace … --continue)

Completion: goal_report status=done summary="…" (preferred). Multi-part goals: first
  goal_report status=continue plan=[{id,title,status:pending}…], then item_id/item_status,
  then status=done when the checklist is complete. Also: GOAL_DONE / GOAL_FAILED markers.
Result: mow goal status --id …  or  $MOW_HOME/goals/<id>.json (summary + plan)
Events: $MOW_HOME/goals/<id>/events.jsonl
Processes: goal_process_start|status|stop (long-lived servers for the goal)
Resume incomplete/failed: mow goal run --id … (reuses state; done goals need reset first)

Failed → re-run: mow goal run --id NAME resumes failed/pending/running; for done use reset then run.

`)
}

func cmdNew(args []string) int {
	fs := cliutil.NewFlagSet("goal new")
	id := fs.String("id", "", "goal id (slug)")
	goalText := fs.String("goal", "", "natural-language objective")
	maxSteps := fs.Int("max-steps", 8, "max Prompt iterations")
	dir := fs.String("dir", "", "store dir (default $MOW_HOME/goals)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	r := &Runner{Store: &Store{Dir: *dir}}
	st, err := r.Create(Spec{ID: *id, Goal: *goalText, MaxSteps: *maxSteps})
	if err != nil {
		fmt.Fprintf(os.Stderr, "mow goal new: %v\n", err)
		return 1
	}
	fmt.Printf("created %s status=%s max_steps=%d\n", st.ID, st.Status, st.MaxSteps)
	fmt.Printf("  %s\n", st.Goal)
	return 0
}

func cmdRun(args []string) int {
	fs := cliutil.NewFlagSet("goal run")
	var ef cliutil.EngineFlags
	ef.Bind(fs)
	id := fs.String("id", "", "existing goal id")
	goalText := fs.String("goal", "", "one-shot goal text (creates/resumes id)")
	maxSteps := fs.Int("max-steps", 8, "max steps when using --goal")
	dir := fs.String("dir", "", "store dir (default $MOW_HOME/goals)")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	// Tool progress + stream/verbose same as mow run/repl.
	eng, err := ef.NewEngineCLI()
	if err != nil {
		fmt.Fprintf(os.Stderr, "mow goal run: %v\n", err)
		return 1
	}
	store := &Store{Dir: *dir}
	r := &Runner{
		Engine: eng,
		Store:  store,
		OnEvent: func(e Event) {
			fmt.Fprintf(os.Stderr, "goal %s %s step=%d/%d %s\n",
				e.State.ID, e.Kind, e.State.Step, e.State.MaxSteps, e.Text)
		},
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	var st State
	switch {
	case strings.TrimSpace(*goalText) != "":
		st, err = r.RunSpec(ctx, Spec{ID: *id, Goal: *goalText, MaxSteps: *maxSteps})
	case strings.TrimSpace(*id) != "":
		st, err = r.Run(ctx, *id)
	default:
		fmt.Fprintln(os.Stderr, "mow goal run: need --id or --goal")
		return 2
	}
	if err != nil && st.Status != StatusDone {
		if errors.Is(err, context.Canceled) {
			fmt.Fprintln(os.Stderr, "mow goal run: cancelled")
		} else {
			fmt.Fprintf(os.Stderr, "mow goal run: %v\n", err)
		}
		printState(st, store)
		return 1
	}
	printState(st, store)
	if st.Status != StatusDone {
		return 1
	}
	return 0
}

func cmdStatus(args []string) int {
	fs := cliutil.NewFlagSet("goal status")
	id := fs.String("id", "", "goal id")
	dir := fs.String("dir", "", "store dir")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if strings.TrimSpace(*id) == "" {
		fmt.Fprintln(os.Stderr, "mow goal status: --id required")
		return 2
	}
	store := &Store{Dir: *dir}
	st, err := store.Load(*id)
	if err != nil {
		fmt.Fprintf(os.Stderr, "mow goal status: %v\n", err)
		fmt.Fprintf(os.Stderr, "  looked in %s (set MOW_HOME or --dir if goals were created elsewhere)\n", store.DirPath())
		return 1
	}
	printState(st, &Store{Dir: *dir})
	return 0
}

func cmdList(args []string) int {
	fs := cliutil.NewFlagSet("goal list")
	dir := fs.String("dir", "", "store dir")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	list, err := (&Store{Dir: *dir}).List()
	if err != nil {
		fmt.Fprintf(os.Stderr, "mow goal list: %v\n", err)
		return 1
	}
	if len(list) == 0 {
		fmt.Println("(no goals)")
		return 0
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tSTATUS\tSTEP\tUPDATED\tGOAL")
	for _, st := range list {
		g := st.Goal
		if len(g) > 48 {
			g = g[:45] + "…"
		}
		fmt.Fprintf(tw, "%s\t%s\t%d/%d\t%s\t%s\n",
			st.ID, st.Status, st.Step, st.MaxSteps,
			st.UpdatedAt.Local().Format("2006-01-02 15:04"), g)
	}
	_ = tw.Flush()
	return 0
}

func cmdDelete(args []string) int {
	fs := cliutil.NewFlagSet("goal delete")
	id := fs.String("id", "", "goal id")
	dir := fs.String("dir", "", "store dir")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if strings.TrimSpace(*id) == "" {
		fmt.Fprintln(os.Stderr, "mow goal delete: --id required")
		return 2
	}
	store := &Store{Dir: *dir}
	if err := store.Delete(*id); err != nil {
		fmt.Fprintf(os.Stderr, "mow goal delete: %v\n", err)
		return 1
	}
	fmt.Printf("deleted %s\n", *id)
	return 0
}

func cmdReset(args []string) int {
	fs := cliutil.NewFlagSet("goal reset")
	id := fs.String("id", "", "goal id")
	dir := fs.String("dir", "", "store dir")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if strings.TrimSpace(*id) == "" {
		fmt.Fprintln(os.Stderr, "mow goal reset: --id required")
		return 2
	}
	store := &Store{Dir: *dir}
	st, err := store.Reset(*id)
	if err != nil {
		fmt.Fprintf(os.Stderr, "mow goal reset: %v\n", err)
		return 1
	}
	fmt.Printf("reset %s status=%s step=0 (run: mow goal run --id %s)\n", st.ID, st.Status, st.ID)
	return 0
}

func printState(st State, store *Store) {
	fmt.Printf("id=%s status=%s step=%d/%d session=%s\n",
		st.ID, st.Status, st.Step, st.MaxSteps, st.SessionID)
	if st.InputTokens > 0 || st.OutputTokens > 0 {
		fmt.Printf("tokens: %d in / %d out\n", st.InputTokens, st.OutputTokens)
	}
	fmt.Printf("goal: %s\n", st.Goal)
	if st.Plan.HasItems() {
		fmt.Printf("plan:\n%s\n", st.Plan.Format())
	}
	if st.Error != "" {
		fmt.Printf("error: %s\n", st.Error)
	}
	if s := strings.TrimSpace(st.Summary); s != "" {
		fmt.Printf("summary:\n%s\n", s)
	}
	// last_reply is often just GOAL_DONE; only show if it differs from summary.
	if lr := strings.TrimSpace(st.LastReply); lr != "" && lr != strings.TrimSpace(st.Summary) {
		fmt.Printf("last_reply:\n%s\n", lr)
	}
	if store != nil && strings.TrimSpace(st.ID) != "" {
		fmt.Printf("file: %s\n", store.Path(st.ID))
		fmt.Printf("events: %s\n", store.eventsPath(st.ID))
	}
	printGoalResume(st)
}

// printGoalResume writes copy-paste resume hints on stderr.
func printGoalResume(st State) {
	id := strings.TrimSpace(st.ID)
	if id == "" {
		return
	}
	fmt.Fprintf(os.Stderr, "resume: mow goal run --id %s\n", id)
	if sid := strings.TrimSpace(st.SessionID); sid != "" {
		fmt.Fprintf(os.Stderr, "        mow repl --session %s\n", sid)
	}
	fmt.Fprintf(os.Stderr, "status: mow goal status --id %s\n", id)
}
