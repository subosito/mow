package agent_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/subosito/mow/internal/agent"
	"github.com/subosito/mow/internal/llm"
)

func TestBeforeFinishContinuesThenFinishes(t *testing.T) {
	var calls int
	var sawFeedback bool
	chat := func(ctx context.Context, messages []llm.Message, tools []llm.ToolSpec) (llm.Message, error) {
		calls++
		last := messages[len(messages)-1]
		if last.Role == "user" && strings.Contains(last.Content, "run the tests") {
			sawFeedback = true
		}
		return llm.Message{Role: "assistant", Content: fmt.Sprintf("answer %d", calls)}, nil
	}
	hooked := 0
	res, err := agent.Run(t.Context(), chat, "go", agent.Options{
		MaxTurns: 10,
		Hooks: agent.Hooks{BeforeFinish: []agent.BeforeFinishFunc{
			func(_ context.Context, e agent.BeforeFinishEvent) (agent.BeforeFinishDecision, error) {
				hooked++
				if e.Continuations == 0 {
					return agent.BeforeFinishDecision{Continue: "run the tests before finishing"}, nil
				}
				return agent.BeforeFinishDecision{}, nil
			},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 || hooked != 2 || !sawFeedback || res.Text != "answer 2" {
		t.Fatalf("calls=%d hooked=%d feedback=%v text=%q", calls, hooked, sawFeedback, res.Text)
	}
}

func TestBeforeFinishIsCapped(t *testing.T) {
	var calls int
	chat := func(ctx context.Context, messages []llm.Message, tools []llm.ToolSpec) (llm.Message, error) {
		calls++
		return llm.Message{Role: "assistant", Content: "done"}, nil
	}
	res, err := agent.Run(t.Context(), chat, "go", agent.Options{
		MaxTurns: 50,
		Hooks: agent.Hooks{BeforeFinish: []agent.BeforeFinishFunc{
			func(context.Context, agent.BeforeFinishEvent) (agent.BeforeFinishDecision, error) {
				return agent.BeforeFinishDecision{Continue: "not yet"}, nil
			},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if calls != agent.MaxFinishContinuations+1 || res.Text != "done" {
		t.Fatalf("calls=%d want %d, text=%q", calls, agent.MaxFinishContinuations+1, res.Text)
	}
}

func TestBeforeFinishErrorAbortsRun(t *testing.T) {
	chat := func(ctx context.Context, messages []llm.Message, tools []llm.ToolSpec) (llm.Message, error) {
		return llm.Message{Role: "assistant", Content: "done"}, nil
	}
	_, err := agent.Run(t.Context(), chat, "go", agent.Options{
		Hooks: agent.Hooks{BeforeFinish: []agent.BeforeFinishFunc{
			func(context.Context, agent.BeforeFinishEvent) (agent.BeforeFinishDecision, error) {
				return agent.BeforeFinishDecision{}, fmt.Errorf("gate broken")
			},
		}},
	})
	if err == nil || !strings.Contains(err.Error(), "gate broken") {
		t.Fatalf("want hook error, got %v", err)
	}
}
