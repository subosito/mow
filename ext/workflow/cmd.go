package workflow

import (
	"context"
	"encoding/json"
	"errors"
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
	ext.RegisterOptionalFeature(ext.OptionalFeature{ID: "workflow", Events: []string{"workflow.run.start", "workflow.phase.start", "workflow.phase.end", "workflow.run.blocked", "workflow.run.done", "workflow.run.failed"}})
	ext.RegisterCommand(ext.Command{Name: "workflow", Summary: "predefined durable workflows", Layer: "ext", Run: runCommand})
}
func runCommand(args []string) int {
	if len(args) == 0 {
		printUsage()
		return 2
	}
	switch args[0] {
	case "list":
		return cmdList(args[1:])
	case "run":
		return cmdRun(args[1:])
	case "resume":
		return cmdResume(args[1:])
	case "status":
		return cmdStatus(args[1:])
	case "definitions":
		return cmdDefinitions(args[1:])
	case "help", "-h", "--help":
		printUsage()
		return 0
	default:
		fmt.Fprintln(os.Stderr, "mow workflow: unknown command", args[0])
		return 2
	}
}
func printUsage() {
	fmt.Fprintln(os.Stderr, `mow workflow — predefined durable procedures

  mow workflow definitions
  mow workflow list [--dir DIR]
  mow workflow run --name NAME --version VERSION --id RUN_ID --input JSON [engine flags]
  mow workflow resume --id RUN_ID [engine flags]
  mow workflow status --id RUN_ID [--dir DIR]

Scheduling is external (systemd/cron/CI); this command is a bounded oneshot.`)
}
func cmdDefinitions(args []string) int {
	if len(args) != 0 {
		return 2
	}
	for _, d := range Definitions() {
		fmt.Printf("%s\t%s\tphases=%d\n", d.Name, d.Version, len(d.Phases))
	}
	return 0
}
func cmdList(args []string) int {
	fs := cliutil.NewFlagSet("workflow list")
	dir := fs.String("dir", "", "store dir")
	if fs.Parse(args) != nil {
		return 2
	}
	list, err := (&Store{Dir: *dir}).List()
	if err != nil {
		fmt.Fprintln(os.Stderr, "mow workflow list:", err)
		return 1
	}
	if len(list) == 0 {
		fmt.Println("(no workflow runs)")
		return 0
	}
	for _, st := range list {
		fmt.Printf("%s\t%s@%s\t%s\t%s\n", st.ID, st.Definition, st.Version, st.Status, st.CurrentPhase)
	}
	return 0
}
func cmdStatus(args []string) int {
	fs := cliutil.NewFlagSet("workflow status")
	id := fs.String("id", "", "run id")
	dir := fs.String("dir", "", "store dir")
	if fs.Parse(args) != nil || strings.TrimSpace(*id) == "" {
		return 2
	}
	st, err := (&Store{Dir: *dir}).Load(*id)
	if err != nil {
		fmt.Fprintln(os.Stderr, "mow workflow status:", err)
		return 1
	}
	printState(st)
	return 0
}
func cmdRun(args []string) int {
	fs := cliutil.NewFlagSet("workflow run")
	var ef cliutil.EngineFlags
	ef.Bind(fs)
	name := fs.String("name", "", "definition name")
	version := fs.String("version", "", "definition version")
	id := fs.String("id", "", "run id")
	input := fs.String("input", "{}", "input JSON")
	dir := fs.String("dir", "", "store dir")
	if fs.Parse(args) != nil || ef.Validate() != nil || *name == "" || *version == "" || *id == "" {
		return 2
	}
	return executeCLI(false, *id, *name, *version, *input, *dir, ef)
}
func cmdResume(args []string) int {
	fs := cliutil.NewFlagSet("workflow resume")
	var ef cliutil.EngineFlags
	ef.Bind(fs)
	id := fs.String("id", "", "run id")
	dir := fs.String("dir", "", "store dir")
	if fs.Parse(args) != nil || ef.Validate() != nil || *id == "" {
		return 2
	}
	return executeCLI(true, *id, "", "", "", *dir, ef)
}
func executeCLI(resume bool, id, name, version, input, dir string, ef cliutil.EngineFlags) int {
	eng, err := mow.New(ef.OptionsCLI())
	if err != nil {
		fmt.Fprintln(os.Stderr, "mow workflow:", err)
		return 1
	}
	defer eng.Close()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	runner := &Runner{Engine: eng, Store: &Store{Dir: dir}, OnEvent: func(e Event) {
		fmt.Fprintf(os.Stderr, "workflow %s %s phase=%s %s\n", e.RunID, e.Type, e.Phase, e.Reason)
	}}
	var st State
	if resume {
		st, err = runner.Resume(ctx, id)
	} else {
		st, err = runner.Run(ctx, id, name, version, json.RawMessage(input))
	}
	printState(st)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			fmt.Fprintln(os.Stderr, "mow workflow: cancelled")
		} else {
			fmt.Fprintln(os.Stderr, "mow workflow:", err)
		}
		return 1
	}
	if st.Status != StatusSucceeded {
		return 1
	}
	return 0
}
func printState(st State) {
	fmt.Printf("run %s definition=%s@%s status=%s", st.ID, st.Definition, st.Version, st.Status)
	if st.CurrentPhase != "" {
		fmt.Printf(" phase=%s", st.CurrentPhase)
	}
	fmt.Println()
	for _, p := range st.Phases {
		fmt.Printf("  %s\t%s\tattempts=%d", p.ID, p.Status, p.Attempts)
		if p.ErrorCode != "" {
			fmt.Printf("\terror=%s", p.ErrorCode)
		}
		fmt.Println()
	}
}
