package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/subosito/mow/slash"
)

const slashUsage = `/workflow list — list durable runs
/workflow definitions — list linked definitions
/workflow run <name> <version> <run-id> [input-json]
/workflow resume <run-id>
/workflow status <run-id>`

func init() {
	slash.Register(slash.Command{Name: "workflow", Summary: "Predefined durable workflows", Usage: slashUsage, Exclusive: true, Run: runSlash})
}
func runSlash(ctx context.Context, req slash.Request) (slash.Result, error) {
	if slash.IsHelpArgs(req.Args) {
		return slash.Result{Title: "/workflow · usage", Body: slashUsage}, nil
	}
	store := &Store{}
	if len(req.Args) == 0 || req.Args[0] == "list" {
		list, err := store.List()
		if err != nil {
			return slash.Result{}, err
		}
		if len(list) == 0 {
			return slash.Result{Title: "workflow · (none)", Body: "Use /workflow definitions, then /workflow run …"}, nil
		}
		var b strings.Builder
		for _, st := range list {
			fmt.Fprintf(&b, "%s\t%s@%s\t%s\t%s\n", st.ID, st.Definition, st.Version, st.Status, st.CurrentPhase)
		}
		return slash.Result{Title: fmt.Sprintf("workflow · %d run(s)", len(list)), Body: strings.TrimSpace(b.String())}, nil
	}
	switch req.Args[0] {
	case "definitions":
		var b strings.Builder
		defs := Definitions()
		for _, d := range defs {
			fmt.Fprintf(&b, "%s\t%s\tphases=%d\n", d.Name, d.Version, len(d.Phases))
		}
		if len(defs) == 0 {
			b.WriteString("(no definitions linked)")
		}
		return slash.Result{Title: "workflow · definitions", Body: strings.TrimSpace(b.String())}, nil
	case "status":
		if len(req.Args) != 2 {
			return slash.Result{}, fmt.Errorf("usage: /workflow status <run-id>")
		}
		st, err := store.Load(req.Args[1])
		if err != nil {
			return slash.Result{}, err
		}
		return workflowStateResult(st), nil
	case "run":
		if req.Engine == nil {
			return slash.Result{}, fmt.Errorf("workflow: no engine in this session")
		}
		if len(req.Args) < 4 || len(req.Args) > 5 {
			return slash.Result{}, fmt.Errorf("usage: /workflow run <name> <version> <run-id> [input-json]")
		}
		input := json.RawMessage(`{}`)
		if len(req.Args) == 5 {
			input = json.RawMessage(req.Args[4])
		}
		st, err := (&Runner{Engine: req.Engine, Store: store}).Run(ctx, req.Args[3], req.Args[1], req.Args[2], input)
		if err != nil {
			return workflowStateResult(st), err
		}
		return workflowStateResult(st), nil
	case "resume":
		if req.Engine == nil {
			return slash.Result{}, fmt.Errorf("workflow: no engine in this session")
		}
		if len(req.Args) != 2 {
			return slash.Result{}, fmt.Errorf("usage: /workflow resume <run-id>")
		}
		st, err := (&Runner{Engine: req.Engine, Store: store}).Resume(ctx, req.Args[1])
		if err != nil {
			return workflowStateResult(st), err
		}
		return workflowStateResult(st), nil
	default:
		return slash.Result{}, fmt.Errorf("unknown workflow command %q", req.Args[0])
	}
}
func workflowStateResult(st State) slash.Result {
	var b strings.Builder
	fmt.Fprintf(&b, "definition: %s@%s\nstatus: %s\n", st.Definition, st.Version, st.Status)
	if st.CurrentPhase != "" {
		fmt.Fprintf(&b, "phase: %s\n", st.CurrentPhase)
	}
	for _, p := range st.Phases {
		fmt.Fprintf(&b, "- %s: %s (attempts %d)", p.ID, p.Status, p.Attempts)
		if p.ErrorCode != "" {
			fmt.Fprintf(&b, " — %s", p.ErrorCode)
		}
		b.WriteByte('\n')
	}
	return slash.Result{Title: "workflow · " + st.ID, Body: strings.TrimSpace(b.String())}
}
