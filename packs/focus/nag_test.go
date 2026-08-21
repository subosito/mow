package focus_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/subosito/mow"

	_ "github.com/subosito/mow/packs/focus"
)

// runExploreTurns drives n explore-only turns and reports every injected
// explore-nag user message. linked=false skips BeforeNew and drops ext hooks
// so the binary-without-this-pack case stays silent.
func runExploreTurns(t *testing.T, turns int, linked bool) []string {
	t.Helper()
	var seq chatSeq
	chat := func(ctx context.Context, messages []mow.Message, tools []mow.ToolSpec) (mow.Message, error) {
		n := seq.next()
		if n > turns {
			return mow.Message{Role: "assistant", Content: "done"}, nil
		}
		return mow.Message{
			Role: "assistant",
			ToolCalls: []mow.ToolCall{{
				ID:   fmt.Sprintf("c%d", n),
				Type: "function",
				// Vary args so the core identical-call guard (sameToolWarnAfter)
				// does not fire and confuse the assertion.
				// bash ls is explore; vary the path so neither the core
				// identical-call guard nor the pack's inventory cap fires
				// as an identical-args repeat (inventory is still the "ls"
				// class, but AfterTurn still counts a denied call as explore).
				Function: mow.FunctionCall{Name: "bash", Arguments: fmt.Sprintf(`{"command":"ls dir%d"}`, n)},
			}},
		}, nil
	}
	opt := mow.Options{
		Workspace:  t.TempDir(),
		AllowShell: true,
		MaxTurns:   turns + 2,
		Chat:       chat,
	}
	if !linked {
		// Hermetic New still runs BeforeNew unless skipped; leftover hooks
		// from a prior Engine in this process would otherwise attach.
		opt.SkipExtensionSetup = true
		opt.DisableExtensionHooks = true
	}
	eng := newEngine(t, opt)
	if _, err := eng.Prompt(context.Background(), "hi"); err != nil {
		t.Fatalf("run: %v", err)
	}
	var synth []string
	for _, m := range eng.Messages() {
		if m.Role == "user" && strings.Contains(m.Content, "consecutive explore-only turns") {
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
