package goal

import (
	"context"
	"fmt"
	"strings"

	"github.com/subosito/mow/slash"
)

// The interactive command deliberately reuses the goal Runner and Store
// instead of spawning `mow goal`: the RPC host supplies the live session
// Engine, and Runner.fire emits the graph.goal.* events that hosts already
// receive through Engine.AddOnEvent.
func init() {
	slash.Register(slash.Command{
		Name:      "goal",
		Summary:   "Multi-step goals — list | new | run | status | …",
		Usage:     goalSlashUsage,
		Exclusive: true,
		Run:       runSlash,
	})
}

const goalSlashUsage = `/goal — list goals
/goal new <id> <goal text…> — create a pending goal
/goal run <id> — run or resume a goal in this session
/goal status <id> — show durable state
/goal reset <id> — reset a goal to pending
/goal delete <id> [--force] — remove a goal
/goal <goal text…> — create and run a one-shot goal

The running forms use the current session Engine and stream graph.goal.* events.`

func runSlash(ctx context.Context, req slash.Request) (slash.Result, error) {
	if slash.IsHelpArgs(req.Args) {
		return slash.Result{Title: "/goal · usage", Body: goalSlashUsage}, nil
	}
	store := &Store{}
	args := req.Args
	if len(args) == 0 || args[0] == "list" || args[0] == "ls" {
		return goalListResult(store)
	}

	switch args[0] {
	case "new":
		if len(args) < 3 {
			return slash.Result{}, fmt.Errorf("usage: /goal new <id> <goal text…>")
		}
		st, err := (&Runner{Store: store}).Create(Spec{ID: args[1], Goal: strings.Join(args[2:], " ")})
		if err != nil {
			return slash.Result{}, fmt.Errorf("goal new: %w", err)
		}
		return stateResult("goal · created "+st.ID, st, store), nil
	case "status":
		if len(args) < 2 {
			return slash.Result{}, fmt.Errorf("usage: /goal status <id>")
		}
		st, err := store.Load(args[1])
		if err != nil {
			return slash.Result{}, fmt.Errorf("goal status: %w", err)
		}
		return stateResult("goal · "+st.ID+" · "+string(st.Status), st, store), nil
	case "reset":
		if len(args) < 2 {
			return slash.Result{}, fmt.Errorf("usage: /goal reset <id>")
		}
		st, err := store.Reset(args[1])
		if err != nil {
			return slash.Result{}, fmt.Errorf("goal reset: %w", err)
		}
		return stateResult("goal · reset "+st.ID, st, store), nil
	case "delete", "remove", "rm":
		if len(args) < 2 {
			return slash.Result{}, fmt.Errorf("usage: /goal delete <id> [--force]")
		}
		force := false
		for _, arg := range args[2:] {
			force = force || arg == "--force" || arg == "-f" || arg == "force"
		}
		if err := store.Remove(args[1], force); err != nil {
			return slash.Result{}, fmt.Errorf("goal delete: %w", err)
		}
		return slash.Result{Title: "goal · deleted " + args[1]}, nil
	case "run":
		if len(args) < 2 {
			return slash.Result{}, fmt.Errorf("usage: /goal run <id>")
		}
		if req.Engine == nil {
			return slash.Result{}, fmt.Errorf("/goal run: no engine in this session")
		}
		st, err := (&Runner{Engine: req.Engine, Store: store}).Run(ctx, args[1])
		return stateResult("goal · "+st.ID+" · "+string(st.Status), st, store), err
	default:
		if req.Engine == nil {
			return slash.Result{}, fmt.Errorf("/goal: no engine in this session")
		}
		st, err := (&Runner{Engine: req.Engine, Store: store}).RunSpec(ctx, Spec{
			ID: args[0], Goal: strings.Join(args, " "),
		})
		return stateResult("goal · "+st.ID+" · "+string(st.Status), st, store), err
	}
}

func goalListResult(store *Store) (slash.Result, error) {
	list, err := store.List()
	if err != nil {
		return slash.Result{}, fmt.Errorf("goal list: %w", err)
	}
	if len(list) == 0 {
		return slash.Result{Title: "goal · (none)", Body: "create one with /goal new <id> <goal text…>"}, nil
	}
	var b strings.Builder
	for _, st := range list {
		fmt.Fprintf(&b, "%s  %s  %d/%d  %s\n", st.ID, st.Status, st.Step, st.MaxSteps, st.Goal)
	}
	return slash.Result{Title: fmt.Sprintf("goals · %d", len(list)), Body: strings.TrimRight(b.String(), "\n")}, nil
}

func stateResult(title string, st State, store *Store) slash.Result {
	var b strings.Builder
	fmt.Fprintf(&b, "id=%s status=%s step=%d/%d\n", st.ID, st.Status, st.Step, st.MaxSteps)
	fmt.Fprintf(&b, "goal: %s\n", st.Goal)
	if st.Summary != "" {
		fmt.Fprintf(&b, "summary: %s\n", st.Summary)
	}
	if st.Partial != "" {
		fmt.Fprintf(&b, "partial: %s\n", st.Partial)
	}
	if st.Error != "" {
		fmt.Fprintf(&b, "error: %s\n", st.Error)
	}
	if store != nil && st.ID != "" {
		fmt.Fprintf(&b, "file: %s", store.Path(st.ID))
	}
	return slash.Result{Title: title, Body: b.String()}
}
