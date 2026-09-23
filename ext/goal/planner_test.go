package goal_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/subosito/mow"
	"github.com/subosito/mow/ext/goal"
)

func TestPlanIsReadOnlyAndDoesNotRunGoal(t *testing.T) {
	home := t.TempDir()
	t.Setenv("MOW_HOME", home)
	calls := 0
	eng, err := mow.New(mow.Options{Workspace: t.TempDir(), NoSession: true, AllowWrite: true, Chat: func(_ context.Context, _ []mow.Message, tools []mow.ToolSpec) (mow.Message, error) {
		calls++
		haveWrite, haveSubmit := false, false
		for _, tool := range tools {
			haveWrite = haveWrite || tool.Function.Name == "write"
			haveSubmit = haveSubmit || tool.Function.Name == "plan_submit"
		}
		if haveWrite || !haveSubmit {
			t.Fatalf("tools write=%v submit=%v", haveWrite, haveSubmit)
		}
		args, _ := json.Marshal(map[string]any{"summary": "plan only", "items": []map[string]any{{"id": "inspect", "title": "Inspect"}, {"id": "fix", "title": "Fix", "worker": "worktree"}, {"id": "verify", "title": "Verify"}}})
		return mow.Message{Role: "assistant", ToolCalls: []mow.ToolCall{{ID: "p", Function: mow.FunctionCall{Name: "plan_submit", Arguments: string(args)}}}}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()
	st, err := (&goal.Runner{Engine: eng, Store: &goal.Store{Dir: t.TempDir()}}).Plan(t.Context(), goal.Spec{ID: "p1", Goal: "fix it"})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 || st.Status != goal.StatusPending || st.Step != 0 || st.Phase != "planned" || len(st.Plan.Items) != 3 {
		t.Fatalf("state=%+v calls=%d", st, calls)
	}
}
