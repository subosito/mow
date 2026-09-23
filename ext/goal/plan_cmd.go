package goal

import (
	"context"
	"fmt"
	"os"

	"github.com/subosito/mow"
	"github.com/subosito/mow/cliutil"
)

func cmdPlan(args []string) int {
	fs := cliutil.NewFlagSet("goal plan")
	var ef cliutil.EngineFlags
	ef.Bind(fs)
	id := fs.String("id", "", "goal id (slug)")
	objective := fs.String("goal", "", "objective to plan")
	maxSteps := fs.Int("max-steps", DefaultMaxSteps, "future execution step budget")
	dir := fs.String("dir", "", "store dir")
	if fs.Parse(args) != nil {
		return 2
	}
	if ef.Validate() != nil {
		return 2
	}
	eng, err := mow.New(ef.OptionsCLI())
	if err != nil {
		fmt.Fprintln(os.Stderr, "mow goal plan:", err)
		return 1
	}
	defer eng.Close()
	st, err := (&Runner{Engine: eng, Store: &Store{Dir: *dir}}).Plan(context.Background(), Spec{ID: *id, Goal: *objective, MaxSteps: *maxSteps})
	if err != nil {
		fmt.Fprintln(os.Stderr, "mow goal plan:", err)
		return 1
	}
	fmt.Printf("planned %s status=%s items=%d\n", st.ID, st.Status, len(st.Plan.Items))
	fmt.Println(st.Plan.Format())
	return 0
}
