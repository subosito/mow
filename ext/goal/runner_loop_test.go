package goal_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/subosito/mow"
	"github.com/subosito/mow/ext/goal"
)

// goal_report must end the Prompt in one chat call (ErrAgentDone) and complete
// the outer goal without requiring a second "GOAL_DONE" text turn.
func TestRunnerGoalReportStopsInnerLoop(t *testing.T) {
	t.Setenv("MOW_HOME", t.TempDir())
	t.Setenv("OPENAI_API_KEY", "k")
	t.Setenv("OPENAI_MODEL", "m")
	dir := t.TempDir()
	var chats atomic.Int32
	eng, err := mow.New(mow.Options{
		Workspace: t.TempDir(),
		NoSession: true,
		Chat: func(ctx context.Context, messages []mow.Message, tools []mow.ToolSpec) (mow.Message, error) {
			n := chats.Add(1)
			if n > 1 {
				// After goal_report, agent must not call chat again this Prompt.
				t.Fatalf("unexpected chat call #%d after goal_report (inner loop should stop)", n)
			}
			// Ensure goal_report is exposed to the model.
			found := false
			for _, ts := range tools {
				if ts.Function.Name == "goal_report" {
					found = true
					break
				}
			}
			if !found {
				t.Fatal("goal_report not in tool specs")
			}
			args, _ := json.Marshal(map[string]string{
				"status":  "done",
				"summary": "all good from report",
			})
			return mow.Message{Role: "assistant", ToolCalls: []mow.ToolCall{{
				ID: "t1", Type: "function",
				Function: mow.FunctionCall{Name: "goal_report", Arguments: string(args)},
			}}}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	r := &goal.Runner{Engine: eng, Store: &goal.Store{Dir: dir}}
	st, err := r.RunSpec(context.Background(), goal.Spec{ID: "rep", Goal: "do it", MaxSteps: 5})
	if err != nil {
		t.Fatal(err)
	}
	if st.Status != goal.StatusDone {
		t.Fatalf("status=%s err=%q", st.Status, st.Error)
	}
	if st.Step != 1 {
		t.Fatalf("step=%d want 1", st.Step)
	}
	if !strings.Contains(st.Summary, "all good from report") {
		t.Fatalf("summary=%q", st.Summary)
	}
	if chats.Load() != 1 {
		t.Fatalf("chats=%d want 1", chats.Load())
	}
}

// Inner unproductive thrash (identical bash forever) must fail the goal quickly.
// Varying new commands is allowed; repeating the same one is not.
func TestRunnerStopsInnerExploreThrash(t *testing.T) {
	t.Setenv("MOW_HOME", t.TempDir())
	t.Setenv("OPENAI_API_KEY", "k")
	t.Setenv("OPENAI_MODEL", "m")
	dir := t.TempDir()
	var chats atomic.Int32
	eng, err := mow.New(mow.Options{
		Workspace: t.TempDir(),
		NoSession: true,
		MaxTurns:  -1,
		Chat: func(ctx context.Context, messages []mow.Message, tools []mow.ToolSpec) (mow.Message, error) {
			n := int(chats.Add(1))
			return mow.Message{Role: "assistant", ToolCalls: []mow.ToolCall{{
				ID:   fmt.Sprintf("b%d", n),
				Type: "function",
				Function: mow.FunctionCall{
					Name:      "bash",
					Arguments: `{"command":"find . -name x"}`, // same every turn
				},
			}}}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	r := &goal.Runner{Engine: eng, Store: &goal.Store{Dir: dir}}
	st, err := r.RunSpec(context.Background(), goal.Spec{ID: "thrash", Goal: "explore forever", MaxSteps: 8})
	if err == nil || st.Status != goal.StatusFailed {
		t.Fatalf("st=%+v err=%v want failed", st, err)
	}
	if st.Step != 1 {
		t.Fatalf("step=%d want 1 (fail inside first Prompt)", st.Step)
	}
	if chats.Load() > 20 {
		t.Fatalf("chats=%d want bounded (~11 unproductive stop)", chats.Load())
	}
	if !strings.Contains(st.Error, "stuck") && !strings.Contains(st.Error, "explor") {
		t.Fatalf("error=%q want stuck/explore wording", st.Error)
	}
}

// Each outer step resets the inner thrash counter. Without an outer guard, a
// model can explore→emit prose→repeat for MaxSteps. This must fail by step 2
// of pure explore-only incomplete steps.
func TestRunnerStopsOuterExploreOnlyStreak(t *testing.T) {
	t.Setenv("MOW_HOME", t.TempDir())
	t.Setenv("OPENAI_API_KEY", "k")
	t.Setenv("OPENAI_MODEL", "m")
	dir := t.TempDir()
	var chats atomic.Int32
	// Per Prompt: one glob then text (ends inner loop without finish).
	// Outer should allow one such step then fail on the second.
	// Only inspect tools after the last user message — prior steps leave tool
	// rows in history and would otherwise skip the tool call forever.
	eng, err := mow.New(mow.Options{
		Workspace: t.TempDir(),
		NoSession: true,
		Chat: func(ctx context.Context, messages []mow.Message, tools []mow.ToolSpec) (mow.Message, error) {
			n := chats.Add(1)
			lastUser := -1
			for i := len(messages) - 1; i >= 0; i-- {
				if messages[i].Role == "user" {
					lastUser = i
					break
				}
			}
			for i := lastUser + 1; i < len(messages); i++ {
				if messages[i].Role == "tool" {
					return mow.Message{Role: "assistant", Content: "still looking around"}, nil
				}
			}
			return mow.Message{Role: "assistant", ToolCalls: []mow.ToolCall{{
				ID:   fmt.Sprintf("g%d", n),
				Type: "function",
				Function: mow.FunctionCall{
					Name:      "glob",
					Arguments: fmt.Sprintf(`{"pattern":"**/*%d.go"}`, n),
				},
			}}}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	r := &goal.Runner{Engine: eng, Store: &goal.Store{Dir: dir}}
	st, err := r.RunSpec(context.Background(), goal.Spec{ID: "outer", Goal: "map the tree", MaxSteps: 8})
	if err == nil || st.Status != goal.StatusFailed {
		t.Fatalf("st=%+v err=%v want failed", st, err)
	}
	// maxExploreOnlySteps is 4 — fail on the 4th pure-explore incomplete step.
	if st.Step != 4 {
		t.Fatalf("step=%d want 4 (fail after 4 explore-only outer steps)", st.Step)
	}
	if !strings.Contains(st.Error, "no progress") && !strings.Contains(st.Error, "explore") {
		t.Fatalf("error=%q", st.Error)
	}
	// 4 steps × (1 tool chat + 1 text chat) = 8 chats.
	if chats.Load() > 12 {
		t.Fatalf("chats=%d want small", chats.Load())
	}
}

// Finish signal must travel through Engine.PromptWith context into goal_report.
func TestRunnerFinishSignalViaContext(t *testing.T) {
	t.Setenv("MOW_HOME", t.TempDir())
	t.Setenv("OPENAI_API_KEY", "k")
	t.Setenv("OPENAI_MODEL", "m")
	dir := t.TempDir()
	eng, err := mow.New(mow.Options{
		Workspace: t.TempDir(),
		NoSession: true,
		Chat: func(ctx context.Context, messages []mow.Message, tools []mow.ToolSpec) (mow.Message, error) {
			args, _ := json.Marshal(map[string]string{
				"status":  "failed",
				"reason":  "blocked by test",
				"summary": "",
			})
			return mow.Message{Role: "assistant", ToolCalls: []mow.ToolCall{{
				ID: "f1", Type: "function",
				Function: mow.FunctionCall{Name: "goal_report", Arguments: string(args)},
			}}}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	r := &goal.Runner{Engine: eng, Store: &goal.Store{Dir: dir}}
	st, err := r.RunSpec(context.Background(), goal.Spec{ID: "failctx", Goal: "x", MaxSteps: 3})
	if err == nil || st.Status != goal.StatusFailed {
		t.Fatalf("st=%+v err=%v", st, err)
	}
	if st.Error != "blocked by test" {
		t.Fatalf("error=%q want blocked by test (finish signal lost?)", st.Error)
	}
}

// Already-done goals must not spend another LLM call.
func TestRunnerDoneIsIdempotent(t *testing.T) {
	t.Setenv("MOW_HOME", t.TempDir())
	dir := t.TempDir()
	store := &goal.Store{Dir: dir}
	if err := store.Save(goal.State{
		ID: "done1", Goal: "x", Status: goal.StatusDone, Step: 1, MaxSteps: 5, Summary: "ok",
	}); err != nil {
		t.Fatal(err)
	}
	var chats atomic.Int32
	eng, err := mow.New(mow.Options{
		NoSession: true,
		Chat: func(ctx context.Context, messages []mow.Message, tools []mow.ToolSpec) (mow.Message, error) {
			chats.Add(1)
			return mow.Message{Role: "assistant", Content: "nope"}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	r := &goal.Runner{Engine: eng, Store: store}
	st, err := r.Run(context.Background(), "done1")
	if err != nil {
		t.Fatal(err)
	}
	if st.Status != goal.StatusDone {
		t.Fatalf("status=%s", st.Status)
	}
	if chats.Load() != 0 {
		t.Fatalf("chats=%d want 0 for already-done", chats.Load())
	}
}

// Productive tool (write) must reset explore-only outer streak so real work can continue.
func TestRunnerProductiveToolResetsExploreStreak(t *testing.T) {
	t.Setenv("MOW_HOME", t.TempDir())
	t.Setenv("OPENAI_API_KEY", "k")
	t.Setenv("OPENAI_MODEL", "m")
	dir := t.TempDir()
	ws := t.TempDir()
	var chats atomic.Int32
	// Step1: glob only → explore streak 1
	// Step2: write → streak 0
	// Step3: GOAL_DONE
	eng, err := mow.New(mow.Options{
		Workspace:  ws,
		NoSession:  true,
		AllowWrite: true,
		Chat: func(ctx context.Context, messages []mow.Message, tools []mow.ToolSpec) (mow.Message, error) {
			n := chats.Add(1)
			for _, m := range messages {
				if m.Role == "tool" {
					// End step after any tool.
					if n >= 5 {
						return mow.Message{Role: "assistant", Content: "done\nGOAL_DONE"}, nil
					}
					return mow.Message{Role: "assistant", Content: "ok continue"}, nil
				}
			}
			switch {
			case n == 1:
				return mow.Message{Role: "assistant", ToolCalls: []mow.ToolCall{{
					ID: "g1", Type: "function",
					Function: mow.FunctionCall{Name: "glob", Arguments: `{"pattern":"*.go"}`},
				}}}, nil
			case n == 3:
				return mow.Message{Role: "assistant", ToolCalls: []mow.ToolCall{{
					ID: "w1", Type: "function",
					Function: mow.FunctionCall{Name: "write", Arguments: `{"path":"a.txt","content":"x"}`},
				}}}, nil
			default:
				return mow.Message{Role: "assistant", Content: "done\nGOAL_DONE"}, nil
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	r := &goal.Runner{Engine: eng, Store: &goal.Store{Dir: dir}}
	st, err := r.RunSpec(context.Background(), goal.Spec{ID: "prod", Goal: "write a file", MaxSteps: 5})
	if err != nil {
		t.Fatalf("err=%v st=%+v", err, st)
	}
	if st.Status != goal.StatusDone {
		t.Fatalf("status=%s error=%q", st.Status, st.Error)
	}
}
