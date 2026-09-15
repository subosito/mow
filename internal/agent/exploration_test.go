package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/subosito/mow/internal/llm"
)

func exploreCall(name, args string) llm.ToolCall {
	return llm.ToolCall{ID: "call", Type: "function", Function: llm.FunctionCall{Name: name, Arguments: args}}
}
func exploreResult(body string) []llm.Message {
	return []llm.Message{{Role: "tool", ToolCallID: "call", Content: body}}
}
func TestExplorationOverlappingReadsRemainAdvisory(t *testing.T) {
	g := explorationGuard{workspace: "/repo"}
	warned := false
	for i := range 100 {
		hint := g.note([]llm.ToolCall{exploreCall("read", fmt.Sprintf(`{"path":"a","offset":%d}`, i))}, exploreResult("same evidence"))
		warned = warned || hint != ""
	}
	if !warned {
		t.Fatal("missing advisory checkpoint")
	}
}
func TestExplorationNovelPagesAndChangedContent(t *testing.T) {
	g := explorationGuard{}
	for i := range 100 {
		c := exploreCall("read", fmt.Sprintf(`{"path":"a","offset":%d}`, i))
		g.note([]llm.ToolCall{c}, exploreResult(fmt.Sprintf("new contents %d", i)))
		if g.low != 0 {
			t.Fatalf("fresh page counted as repetition at %d", i)
		}
	}
}
func TestExplorationMissesAndProductiveReset(t *testing.T) {
	for _, name := range []string{"grep", "glob"} {
		t.Run(name, func(t *testing.T) {
			g := explorationGuard{}
			for i := range 2 {
				g.note([]llm.ToolCall{exploreCall(name, fmt.Sprintf(`{"pattern":"missing%d"}`, i))}, exploreResult("(no matches)"))
			}
			if !g.warned {
				t.Fatal("distinct misses should prompt correction")
			}
			g.note([]llm.ToolCall{exploreCall("edit", `{}`)}, exploreResult("error: old text not found"))
			if !g.warned {
				t.Fatal("failed edit reset the guard")
			}
			g.note([]llm.ToolCall{exploreCall("edit", `{}`)}, exploreResult("edited a"))
			if g.warned || g.rounds != 0 {
				t.Fatal("successful edit did not reset exploration")
			}
		})
	}
}
func TestExplorationBashInventoryAndValidation(t *testing.T) {
	g := explorationGuard{}
	for range 6 {
		g.note([]llm.ToolCall{exploreCall("bash", `{"command":"git status"}`)}, exploreResult("unchanged"))
	}
	if !g.warned {
		t.Fatal("inventory escaped checkpoint")
	}
	g.note([]llm.ToolCall{exploreCall("bash", `{"command":"devenv shell -- go test ./..."}`)}, exploreResult("ok"))
	if g.rounds != 0 {
		t.Fatal("validation did not reset exploration")
	}
}

type explorationRead struct{ runs int }

func (t *explorationRead) Name() string                { return "read" }
func (t *explorationRead) Description() string         { return "read" }
func (t *explorationRead) Parameters() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (t *explorationRead) Exec(_ context.Context, _ json.RawMessage) (string, error) {
	t.runs++
	return "same page", nil
}

func TestExplorationMissesContinueThroughLoop(t *testing.T) {
	tool := &explorationRead{}
	calls := 0
	chat := func(_ context.Context, _ []llm.Message, specs []llm.ToolSpec) (llm.Message, error) {
		calls++
		if len(specs) == 0 {
			t.Fatal("checkpoint removed tools")
		}
		if calls == 31 {
			return llm.Message{Role: "assistant", Content: "done"}, nil
		}
		return llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{exploreCall("read", fmt.Sprintf(`{"path":"a","offset":%d}`, calls))}}, nil
	}
	res, err := Run(t.Context(), chat, "investigate", Options{Tools: []Tool{tool}})
	if err != nil || res.Text != "done" || tool.runs != 30 {
		t.Fatalf("result=%+v err=%v calls=%d", res, err, calls)
	}
}
func TestExplorationElapsedCheckpoint(t *testing.T) {
	now := time.Now()
	g := explorationGuard{started: now, now: func() time.Time { return now }}
	call := exploreCall("read", `{"path":"a"}`)
	now = now.Add(explorationWarnTime)
	hint := g.note([]llm.ToolCall{call}, exploreResult("new evidence"))
	if hint != explorationHint {
		t.Fatal("missing time checkpoint")
	}
	now = now.Add(time.Hour)
	g.note([]llm.ToolCall{call}, exploreResult("more new evidence"))
	g.note([]llm.ToolCall{exploreCall("write", `{}`)}, exploreResult("written"))
	if g.started != now {
		t.Fatal("productive work did not reset elapsed exploration")
	}
	g.note([]llm.ToolCall{call}, exploreResult("edited contents"))
}
