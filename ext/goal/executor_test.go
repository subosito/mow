package goal_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/subosito/mow"
	"github.com/subosito/mow/ext/goal"
)

func TestExecutorPlanThenDone(t *testing.T) {
	t.Setenv("MOW_HOME", t.TempDir())
	t.Setenv("OPENAI_API_KEY", "k")
	t.Setenv("OPENAI_MODEL", "m")
	dir := t.TempDir()
	phase := 0
	eng, err := mow.New(mow.Options{
		Workspace: t.TempDir(),
		NoSession: true,
		Chat: func(ctx context.Context, messages []mow.Message, tools []mow.ToolSpec) (mow.Message, error) {
			// Ensure process tools are present.
			var names []string
			for _, ts := range tools {
				names = append(names, ts.Function.Name)
			}
			joined := strings.Join(names, ",")
			if !strings.Contains(joined, "goal_report") || !strings.Contains(joined, "goal_process_start") {
				t.Fatalf("tools=%v", names)
			}
			phase++
			if phase == 1 {
				args, _ := json.Marshal(map[string]any{
					"status": "continue",
					"plan": []map[string]string{
						{"id": "a", "title": "Do A", "status": "pending"},
						{"id": "b", "title": "Do B", "status": "pending"},
					},
				})
				return mow.Message{Role: "assistant", ToolCalls: []mow.ToolCall{{
					ID: "t1", Type: "function",
					Function: mow.FunctionCall{Name: "goal_report", Arguments: string(args)},
				}}}, nil
			}
			if phase == 2 {
				args, _ := json.Marshal(map[string]string{
					"status": "continue", "item_id": "a", "item_status": "done",
				})
				return mow.Message{Role: "assistant", ToolCalls: []mow.ToolCall{{
					ID: "t2", Type: "function",
					Function: mow.FunctionCall{Name: "goal_report", Arguments: string(args)},
				}}}, nil
			}
			if phase == 3 {
				args, _ := json.Marshal(map[string]string{
					"status": "continue", "item_id": "b", "item_status": "done",
				})
				return mow.Message{Role: "assistant", ToolCalls: []mow.ToolCall{{
					ID: "t3", Type: "function",
					Function: mow.FunctionCall{Name: "goal_report", Arguments: string(args)},
				}}}, nil
			}
			args, _ := json.Marshal(map[string]string{
				"status": "done", "summary": "A and B complete",
			})
			return mow.Message{Role: "assistant", ToolCalls: []mow.ToolCall{{
				ID: "t4", Type: "function",
				Function: mow.FunctionCall{Name: "goal_report", Arguments: string(args)},
			}}}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	r := &goal.Runner{Engine: eng, Store: &goal.Store{Dir: dir}}
	st, err := r.RunSpec(context.Background(), goal.Spec{ID: "plan1", Goal: "do A and B", MaxSteps: 8})
	if err != nil {
		t.Fatalf("err=%v st=%+v", err, st)
	}
	if st.Status != goal.StatusDone {
		t.Fatalf("status=%s plan=%s", st.Status, st.Plan.Format())
	}
	if !st.Plan.AllDone() || len(st.Plan.Items) != 2 {
		t.Fatalf("plan=%+v", st.Plan)
	}
	if !strings.Contains(st.Summary, "A and B") {
		t.Fatalf("summary=%q", st.Summary)
	}
}

func TestExecutorRejectsDoneWithOpenChecklist(t *testing.T) {
	t.Setenv("MOW_HOME", t.TempDir())
	t.Setenv("OPENAI_API_KEY", "k")
	t.Setenv("OPENAI_MODEL", "m")
	dir := t.TempDir()
	n := 0
	eng, err := mow.New(mow.Options{
		Workspace: t.TempDir(),
		NoSession: true,
		Chat: func(ctx context.Context, messages []mow.Message, tools []mow.ToolSpec) (mow.Message, error) {
			n++
			if n == 1 {
				args, _ := json.Marshal(map[string]any{
					"status": "continue",
					"plan":   []map[string]string{{"id": "x", "title": "X", "status": "pending"}},
				})
				return mow.Message{Role: "assistant", ToolCalls: []mow.ToolCall{{
					ID: "t1", Type: "function",
					Function: mow.FunctionCall{Name: "goal_report", Arguments: string(args)},
				}}}, nil
			}
			// Illegal early done.
			args, _ := json.Marshal(map[string]string{"status": "done", "summary": "nope"})
			return mow.Message{Role: "assistant", ToolCalls: []mow.ToolCall{{
				ID: "t2", Type: "function",
				Function: mow.FunctionCall{Name: "goal_report", Arguments: string(args)},
			}}}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	r := &goal.Runner{Engine: eng, Store: &goal.Store{Dir: dir}}
	// MaxSteps 2: plan + rejected done → continue → max steps fail (not status done)
	st, err := r.RunSpec(context.Background(), goal.Spec{ID: "rej", Goal: "x", MaxSteps: 2})
	if st.Status == goal.StatusDone {
		t.Fatalf("must not be done with open checklist: %+v err=%v", st, err)
	}
}
