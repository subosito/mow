package engine_test

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/subosito/mow"
)

// The decisive question behind "compact drops ctx% to 1%, then it climbs back
// to ~50% after one interaction": does compaction actually shrink the message
// list sent to the provider on the NEXT call, or only the reported number?
//
// This measures the wire directly and ignores estimates entirely.
func TestCompactShrinksNextRequestPayload(t *testing.T) {
	var mu sync.Mutex
	var sentChars []int

	eng, err := mow.New(mow.Options{
		NoSession: true,
		Chat: func(ctx context.Context, messages []mow.Message, tools []mow.ToolSpec) (mow.Message, error) {
			n := 0
			for _, m := range messages {
				n += len(m.Content)
			}
			mu.Lock()
			sentChars = append(sentChars, n)
			mu.Unlock()
			return mow.Message{
				Role:    "assistant",
				Content: "ok",
				Usage:   mow.Usage{InputTokens: n / 4, OutputTokens: 5},
			}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	for i := 0; i < 20; i++ {
		if _, err := eng.Prompt(context.Background(), strings.Repeat("x", 500)); err != nil {
			t.Fatal(err)
		}
	}
	mu.Lock()
	sentBeforeCompact := sentChars[len(sentChars)-1]
	mu.Unlock()

	rep, err := eng.Compact(2000)
	if err != nil {
		t.Fatal(err)
	}
	if rep.CharsSaved <= 0 {
		t.Fatalf("expected savings: %+v", rep)
	}

	// Next turn: short prompt, so any growth is history, not new input.
	if _, err := eng.Prompt(context.Background(), "hi"); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	sentAfterCompact := sentChars[len(sentChars)-1]
	mu.Unlock()

	t.Logf("wire chars: lastCallBeforeCompact=%d firstCallAfterCompact=%d "+
		"(compact reported CharsBefore=%d CharsAfter=%d saved=%d)",
		sentBeforeCompact, sentAfterCompact, rep.CharsBefore, rep.CharsAfter, rep.CharsSaved)

	if sentAfterCompact >= sentBeforeCompact {
		t.Fatalf("compaction did NOT shrink the next request: before=%d after=%d",
			sentBeforeCompact, sentAfterCompact)
	}
	t.Logf("OK: next request shrank by %d chars (%.0f%%)",
		sentBeforeCompact-sentAfterCompact,
		100*float64(sentBeforeCompact-sentAfterCompact)/float64(sentBeforeCompact))
}

// Traces the number a host header would show across compact-then-interact,
// which is what the user actually sees. Compaction can be working on the wire
// while this number still tells a confusing story.
func TestContextTokensAcrossCompactAndNextTurn(t *testing.T) {
	// Realistic shape: a large fixed preamble (system + tool schemas) that
	// compaction cannot touch, plus history.
	const preambleChars = 60_000

	eng, err := mow.New(mow.Options{
		NoSession: true,
		Chat: func(ctx context.Context, messages []mow.Message, tools []mow.ToolSpec) (mow.Message, error) {
			n := preambleChars
			for _, m := range messages {
				n += len(m.Content)
			}
			return mow.Message{
				Role:    "assistant",
				Content: "ok",
				Usage:   mow.Usage{InputTokens: n / 4, OutputTokens: 5},
			}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	for i := 0; i < 20; i++ {
		if _, err := eng.Prompt(context.Background(), strings.Repeat("x", 2000)); err != nil {
			t.Fatal(err)
		}
	}
	before := eng.ContextTokens()

	rep, err := eng.Compact(0) // auto budget, like /compact with no arg
	if err != nil {
		t.Fatal(err)
	}
	afterCompact := eng.ContextTokens()

	if _, err := eng.Prompt(context.Background(), "hi"); err != nil {
		t.Fatal(err)
	}
	afterTurn := eng.ContextTokens()

	t.Logf("header would show: before=%d afterCompact=%d afterNextTurn=%d",
		before, afterCompact, afterTurn)
	t.Logf("compact report: layer=%s before=%d after=%d saved=%d",
		rep.Layer, rep.CharsBefore, rep.CharsAfter, rep.CharsSaved)

	if afterCompact == 0 {
		t.Fatal("afterCompact is 0: header would read 0% and any later value looks like a rebound")
	}
	if afterCompact >= before {
		t.Fatalf("compact did not lower the reported context: before=%d after=%d", before, afterCompact)
	}
	// The regression this guards: the post-compact estimate omitted the fixed
	// per-request overhead, so the header read near-empty and then appeared to
	// "rebound" on the next real measurement. A short interaction adds almost
	// no history, so the two numbers must stay close.
	if float64(afterTurn) > 1.25*float64(afterCompact) {
		t.Fatalf("post-compact estimate rebounds: afterCompact=%d afterTurn=%d (%.1fx); "+
			"the estimate is likely missing fixed request overhead",
			afterCompact, afterTurn, float64(afterTurn)/float64(afterCompact))
	}
}
