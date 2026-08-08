package engine

import (
	"strings"
	"testing"

	"github.com/subosito/mow/internal/llm"
)

func TestRenderHistoryForSummary(t *testing.T) {
	t.Parallel()

	t.Run("serializes roles and clips tool bodies", func(t *testing.T) {
		t.Parallel()
		huge := strings.Repeat("x", summaryInputToolChars*3)
		body, prev := renderHistoryForSummary([]llm.Message{
			// The system prompt is re-sent on every request anyway; paying to
			// summarize it would be paying for it twice.
			{Role: "system", Content: "you are mow"},
			{Role: "user", Content: "fix the parser"},
			{Role: "assistant", Content: "looking", ToolCalls: []llm.ToolCall{{
				Function: llm.FunctionCall{Name: "read", Arguments: `{"path":"p.go"}`},
			}}},
			{Role: "tool", Name: "read", Content: huge},
		})
		if prev {
			t.Error("no earlier summary present, but one was reported")
		}
		if strings.Contains(body, "you are mow") {
			t.Error("system prompt must not be summarized — it is re-sent every turn")
		}
		for _, want := range []string{"[User]", "fix the parser", "[Assistant]", "[Tool call] read", "[Tool result] read"} {
			if !strings.Contains(body, want) {
				t.Errorf("missing %q in:\n%s", want, body)
			}
		}
		// Tool bodies dominate history; without a clip the summary call itself
		// becomes one of the most expensive requests of the run.
		if len(body) > summaryInputToolChars*2 {
			t.Errorf("tool body not clipped: body is %d bytes", len(body))
		}
		if !strings.Contains(body, "more bytes)") {
			t.Error("clip must say how much was dropped")
		}
	})

	t.Run("detects an earlier summary", func(t *testing.T) {
		t.Parallel()
		body, prev := renderHistoryForSummary([]llm.Message{
			{Role: "user", Content: compactStubMarker + " to fit the model window]\nGoal: ship it"},
			{Role: "user", Content: "keep going"},
		})
		if !prev {
			t.Fatal("want prev=true so the merge-forward instruction is added")
		}
		if !strings.Contains(body, "[Earlier summary]") {
			t.Errorf("earlier summary not labelled:\n%s", body)
		}
	})

	t.Run("empty history yields empty body", func(t *testing.T) {
		t.Parallel()
		body, _ := renderHistoryForSummary(nil)
		if strings.TrimSpace(body) != "" {
			t.Errorf("want empty, got %q", body)
		}
	})
}

// The summary rides in context on every later turn, so an unbounded one would
// defeat the purpose of compacting at all.
func TestSummaryPromptShape(t *testing.T) {
	t.Parallel()
	for _, want := range []string{"## Goal", "## Constraints", "## Progress", "## Key decisions", "## Next steps", "## Critical context"} {
		if !strings.Contains(summarySystemPrompt, want) {
			t.Errorf("summary prompt missing section %q", want)
		}
	}
	if !strings.Contains(summaryUpdateSuffix, "PRESERVE") {
		t.Error("update suffix must instruct merge-forward, or successive summaries decay")
	}
	if summaryInputToolChars >= summaryMaxChars {
		t.Error("per-tool input clip should be well under the output cap")
	}
}

func TestClip(t *testing.T) {
	t.Parallel()
	if got := clip("short", 100); got != "short" {
		t.Errorf("under-limit input altered: %q", got)
	}
	got := clip(strings.Repeat("a", 50), 10)
	if !strings.HasPrefix(got, strings.Repeat("a", 10)) {
		t.Errorf("want the first 10 bytes kept, got %q", got)
	}
	if !strings.Contains(got, "40 more bytes") {
		t.Errorf("want the dropped byte count named, got %q", got)
	}
}

// A one-shot call must not write a prompt-cache entry: nothing will ever read
// it back, and a cache write bills above plain input.
func TestOneShotDisablesCache(t *testing.T) {
	t.Parallel()
	base := &llm.Client{Model: "m", PromptCache: true, CacheTTL: "1h"}
	one := base.OneShot()
	if one.PromptCache || one.CacheTTL != "" {
		t.Errorf("one-shot client still caches: PromptCache=%v TTL=%q", one.PromptCache, one.CacheTTL)
	}
	// The original must be untouched — the main conversation still wants its
	// cached prefix.
	if !base.PromptCache || base.CacheTTL != "1h" {
		t.Error("OneShot mutated the source client")
	}
	if one.Model != base.Model {
		t.Error("OneShot should preserve everything except caching")
	}
}
