package goal

import (
	"context"
	"fmt"
	"strings"

	"github.com/subosito/mow/slash"
)

func runPlanSlash(ctx context.Context, req slash.Request) (slash.Result, error) {
	if slash.IsHelpArgs(req.Args) {
		return slash.Result{Title: "/plan · usage", Body: "/plan <id> <objective…> — create a durable read-only plan; execute later with /goal run <id>"}, nil
	}
	if req.Engine == nil {
		return slash.Result{}, fmt.Errorf("/plan: no engine in this session")
	}
	if len(req.Args) < 2 {
		return slash.Result{}, fmt.Errorf("usage: /plan <id> <objective…>")
	}
	st, err := (&Runner{Engine: req.Engine, Store: &Store{}}).Plan(ctx, Spec{ID: req.Args[0], Goal: strings.Join(req.Args[1:], " ")})
	if err != nil {
		return slash.Result{}, err
	}
	return stateResult("plan · "+st.ID+" · ready", st, &Store{}), nil
}
