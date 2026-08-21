package focus_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/subosito/mow/ext/focus"
	"github.com/subosito/mow/internal/agent"
	"github.com/subosito/mow/internal/llm"
)

// lsTool stands in for bash: `ls <dir>` is an explore command: calling it repeatedly should trip the
// explore-streak nag once the pack is installed.
type lsTool struct{ n int }

func (t *lsTool) Name() string        { return "bash" }
func (t *lsTool) Description() string { return "list" }
func (t *lsTool) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"command":{"type":"string"}}}`)
}
func (t *lsTool) Exec(_ context.Context, _ json.RawMessage) (string, error) {
	t.n++
	return "a\nb\n", nil
}

// runExploreTurns drives n explore-only turns and reports every synthetic user
// message the loop injected.
func runExploreTurns(t *testing.T, turns int, install bool) []string {
	t.Helper()
	n := 0
	chat := func(ctx context.Context, messages []llm.Message, tools []llm.ToolSpec) (llm.Message, error) {
		n++
		if n > turns {
			return llm.Message{Role: "assistant", Content: "done"}, nil
		}
		return llm.Message{
			Role: "assistant",
			ToolCalls: []llm.ToolCall{{
				ID:   fmt.Sprintf("c%d", n),
				Type: "function",
				// Vary args so the core identical-call guard (sameToolWarnAfter)
				// does not fire and confuse the assertion.
				// bash ls is explore; vary the path so neither the core
				// identical-call guard nor the pack's inventory cap fires.
				Function: llm.FunctionCall{Name: "bash", Arguments: fmt.Sprintf(`{"command":"ls dir%d"}`, n)},
			}},
		}, nil
	}
	opt := agent.Options{MaxTurns: turns + 2, MaxParallelTools: 1, Tools: []agent.Tool{&lsTool{}}}
	if install {
		focus.InstallForTest(&opt, focus.Config{})
	}
	res, err := agent.Run(context.Background(), chat, "hi", opt)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	var synth []string
	for _, m := range res.Messages {
		if m.Role == "user" && m.Synthetic {
			synth = append(synth, m.Content)
		}
	}
	return synth
}

func nagCount(msgs []string) int {
	n := 0
	for _, m := range msgs {
		if strings.Contains(m, "consecutive explore-only turns") {
			n++
		}
	}
	return n
}

// The nag must fire once the streak reaches the threshold — and must not fire
// before it.
func TestExploreNagFiresAtThresholdWhenLinked(t *testing.T) {
	if got := nagCount(runExploreTurns(t, 5, true)); got != 0 {
		t.Fatalf("5 explore turns: nag count=%d want 0 (threshold is 6)", got)
	}
	if got := nagCount(runExploreTurns(t, 6, true)); got == 0 {
		t.Fatal("6 explore turns: want the explore nag, got none")
	}
}

// The whole point of the extraction: without the pack the loop is silent, so
// removing the blank import removes the behavior with no core residue.
func TestExploreNagAbsentWhenUnlinked(t *testing.T) {
	for _, turns := range []int{6, 9} {
		if got := nagCount(runExploreTurns(t, turns, false)); got != 0 {
			t.Fatalf("unlinked, %d turns: nag count=%d want 0", turns, got)
		}
	}
}
