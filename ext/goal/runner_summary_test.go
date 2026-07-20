package goal_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/subosito/mow"
	"github.com/subosito/mow/ext/goal"
)

func TestRunnerSummaryFromGoalReport(t *testing.T) {
	t.Setenv("MOW_HOME", t.TempDir())
	t.Setenv("OPENAI_API_KEY", "k")
	t.Setenv("OPENAI_MODEL", "m")
	dir := t.TempDir()
	phase := 0
	eng, err := mow.New(mow.Options{
		Workspace: t.TempDir(),
		NoSession: true,
		Chat: func(ctx context.Context, messages []mow.Message, tools []mow.ToolSpec) (mow.Message, error) {
			for _, m := range messages {
				if m.Role == "tool" {
					return mow.Message{Role: "assistant", Content: "GOAL_DONE"}, nil
				}
			}
			if phase == 0 {
				phase = 1
				args, _ := json.Marshal(map[string]string{
					"status":  "done",
					"summary": "bullets from report\n- a\n- b",
				})
				return mow.Message{Role: "assistant", ToolCalls: []mow.ToolCall{{
					ID: "t1", Type: "function",
					Function: mow.FunctionCall{Name: "goal_report", Arguments: string(args)},
				}}}, nil
			}
			return mow.Message{Role: "assistant", Content: "GOAL_DONE"}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	r := &goal.Runner{Engine: eng, Store: &goal.Store{Dir: dir}}
	st, err := r.RunSpec(context.Background(), goal.Spec{ID: "g1", Goal: "summarize", MaxSteps: 3})
	if err != nil {
		t.Fatal(err)
	}
	if st.Status != goal.StatusDone {
		t.Fatalf("status=%s", st.Status)
	}
	if !strings.Contains(st.Summary, "bullets from report") {
		t.Fatalf("summary=%q", st.Summary)
	}
}

// When the model writes prose then finishes with only GOAL_DONE (no summary arg),
// pick the prose from the full message history.
func TestRunnerSummaryFromIntermediateAssistant(t *testing.T) {
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
			// turn 1: prose without tools (agent ends this Prompt if no tool calls)
			// That would end the step without done. So: prose with GOAL_DONE on same message works:
			if n == 1 {
				return mow.Message{Role: "assistant", Content: "Here are 3 bullets:\n- one\n- two\n- three\nGOAL_DONE"}, nil
			}
			return mow.Message{Role: "assistant", Content: "GOAL_DONE"}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	r := &goal.Runner{Engine: eng, Store: &goal.Store{Dir: dir}}
	st, err := r.RunSpec(context.Background(), goal.Spec{ID: "g2", Goal: "summarize", MaxSteps: 3})
	if err != nil {
		t.Fatal(err)
	}
	if st.Status != goal.StatusDone {
		t.Fatalf("status=%s", st.Status)
	}
	if !strings.Contains(st.Summary, "one") || strings.TrimSpace(st.Summary) == "GOAL_DONE" {
		t.Fatalf("summary=%q", st.Summary)
	}
}
