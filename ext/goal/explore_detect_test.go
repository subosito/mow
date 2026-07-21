package goal

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/subosito/mow"
)

func TestLastStepExploreOnlyDetection(t *testing.T) {
	t.Setenv("MOW_HOME", t.TempDir())
	t.Setenv("OPENAI_API_KEY", "k")
	t.Setenv("OPENAI_MODEL", "m")
	n := 0
	eng, err := mow.New(mow.Options{
		Workspace: t.TempDir(),
		NoSession: true,
		Chat: func(ctx context.Context, messages []mow.Message, tools []mow.ToolSpec) (mow.Message, error) {
			n++
			for _, m := range messages {
				if m.Role == "tool" {
					return mow.Message{Role: "assistant", Content: "still looking"}, nil
				}
			}
			return mow.Message{Role: "assistant", ToolCalls: []mow.ToolCall{{
				ID:   fmt.Sprintf("g%d", n),
				Type: "function",
				Function: mow.FunctionCall{
					Name:      "glob",
					Arguments: `{"pattern":"**/*.go"}`,
				},
			}}}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := eng.Prompt(context.Background(), "Begin work"); err != nil {
		t.Fatal(err)
	}
	for i, m := range eng.Messages() {
		var tc string
		for _, c := range m.ToolCalls {
			tc += c.Function.Name + ","
		}
		t.Logf("[%d] role=%s name=%q tools=%s content=%q", i, m.Role, m.Name, tc, trunc(m.Content, 40))
	}
	if !lastStepExploreOnly(eng) {
		t.Fatal("expected explore-only after glob-only step")
	}

	// Second prompt with write should not be explore-only.
	n = 0
	eng2, err := mow.New(mow.Options{
		Workspace:  t.TempDir(),
		NoSession:  true,
		AllowWrite: true,
		Chat: func(ctx context.Context, messages []mow.Message, tools []mow.ToolSpec) (mow.Message, error) {
			for _, m := range messages {
				if m.Role == "tool" {
					return mow.Message{Role: "assistant", Content: "wrote"}, nil
				}
			}
			return mow.Message{Role: "assistant", ToolCalls: []mow.ToolCall{{
				ID: "w1", Type: "function",
				Function: mow.FunctionCall{Name: "write", Arguments: `{"path":"a.txt","content":"x"}`},
			}}}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := eng2.Prompt(context.Background(), "write it"); err != nil {
		t.Fatal(err)
	}
	if lastStepExploreOnly(eng2) {
		t.Fatal("write step should not be explore-only")
	}
}

func trunc(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// Reproduces the runner path: two incomplete explore-only steps with SystemAppend.
func TestRunnerPathExploreOnlyStreak(t *testing.T) {
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
	r := &Runner{Engine: eng, Store: &Store{Dir: dir}}
	st, err := r.RunSpec(context.Background(), Spec{ID: "outer2", Goal: "map the tree", MaxSteps: 8})
	if err == nil {
		t.Fatal("want error")
	}
	if st.Status != StatusFailed || st.Step != 4 {
		t.Fatalf("want failed at step 4, got status=%s step=%d error=%q", st.Status, st.Step, st.Error)
	}
	if !strings.Contains(st.Error, "no progress") {
		t.Fatalf("error=%q", st.Error)
	}
}
