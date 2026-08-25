package agent

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/subosito/mow/internal/llm"
)

func TestCompact(t *testing.T) {
	var msgs []llm.Message
	msgs = append(msgs, llm.Message{Role: "system", Content: "sys"})
	for i := 0; i < 20; i++ {
		msgs = append(msgs, llm.Message{Role: "user", Content: strings.Repeat("x", 100)})
		msgs = append(msgs, llm.Message{Role: "assistant", Content: strings.Repeat("y", 100)})
	}
	out := CompactOpts(msgs, 2000, "", DefaultMaxToolResultChars)
	if EstChars(out) >= EstChars(msgs) {
		t.Fatalf("expected smaller: in=%d out=%d", EstChars(msgs), EstChars(out))
	}
	if out[0].Role != "system" {
		t.Fatalf("want system first, got %s", out[0].Role)
	}
}

func TestTruncateToolResult(t *testing.T) {
	s := strings.Repeat("a\n", 1000)
	out := TruncateToolResult(s, 100)
	if len(out) > 120 {
		t.Fatalf("len=%d", len(out))
	}
	if !strings.Contains(out, "truncated") {
		t.Fatalf("missing marker: %q", out)
	}
	if TruncateToolResult("short", 100) != "short" {
		t.Fatal("short should pass")
	}
}

func TestCompactTrimsHugeToolResults(t *testing.T) {
	msgs := []llm.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "read it"},
		{Role: "assistant", Content: "", ToolCalls: []llm.ToolCall{{
			ID: "1", Type: "function",
			Function: llm.FunctionCall{Name: "read", Arguments: `{}`},
		}}},
		{Role: "tool", ToolCallID: "1", Name: "read", Content: strings.Repeat("Z", 80_000)},
		{Role: "user", Content: "thanks"},
	}
	// Under budget but tool dump must still shrink.
	out := CompactOpts(msgs, 200_000, "", 5_000)
	var tool string
	for _, m := range out {
		if m.Role == "tool" {
			tool = m.Content
		}
	}
	if len(tool) > 6_000 {
		t.Fatalf("tool still huge: %d", len(tool))
	}
	if !strings.Contains(tool, "truncated") {
		t.Fatalf("expected truncation marker")
	}
}

func TestCompactDropsMiddle(t *testing.T) {
	var msgs []llm.Message
	msgs = append(msgs, llm.Message{Role: "system", Content: "sys"})
	for i := 0; i < 40; i++ {
		msgs = append(msgs, llm.Message{Role: "user", Content: strings.Repeat("u", 200)})
		msgs = append(msgs, llm.Message{Role: "assistant", Content: strings.Repeat("a", 200)})
	}
	// Tiny budget forces drop.
	out := CompactOpts(msgs, 3_000, "SUMMARY_HERE", 2_000)
	if EstChars(out) > 8_000 {
		t.Fatalf("still too large: %d", EstChars(out))
	}
	found := false
	for _, m := range out {
		if strings.Contains(m.Content, "SUMMARY_HERE") || strings.Contains(m.Content, "compacted") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected compact stub in %v", out)
	}
	// Last user should remain.
	lastUser := ""
	for _, m := range out {
		if m.Role == "user" {
			lastUser = m.Content
		}
	}
	if !strings.HasPrefix(lastUser, "uuu") && lastUser != "SUMMARY_HERE" && !strings.Contains(lastUser, "compacted") {
		// last real user is all u's
		if len(lastUser) < 10 {
			t.Fatalf("unexpected last user %q", lastUser)
		}
	}
}

func TestCompactNoOrphanToolResults(t *testing.T) {
	// Tool-heavy single-prompt run: one user message at the start, then only
	// assistant(tool_calls) + tool pairs. After the middle is dropped the kept
	// window must not start with a tool result whose tool_use was cut.
	msgs := []llm.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "do the thing"},
	}
	for i := 0; i < 30; i++ {
		msgs = append(msgs, llm.Message{
			Role: "assistant",
			ToolCalls: []llm.ToolCall{{ID: "id", Type: "function",
				Function: llm.FunctionCall{Name: "read", Arguments: `{}`}}},
		})
		msgs = append(msgs, llm.Message{Role: "tool", ToolCallID: "id", Name: "read",
			Content: strings.Repeat("x", 400)})
	}
	out := CompactOpts(msgs, 3_000, "", 2_000)
	// Skip system and the summary stub; the first remaining message must not
	// be a tool result.
	i := 0
	for i < len(out) && (out[i].Role == "system" || out[i].Role == "user") {
		i++
	}
	if i < len(out) && out[i].Role == "tool" {
		t.Fatalf("kept window starts with orphan tool result: %+v", out[i])
	}
}

func TestCompactStubPreservesTask(t *testing.T) {
	// Long tool thrash after a clear user goal: task anchors must keep the
	// original request even when the kept window is only tool noise + "hi".
	msgs := []llm.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "hi"},
		{Role: "assistant", Content: "hello"},
		{Role: "user", Content: "Please fix the line_hash bug in tui.go and add a test"},
	}
	for i := 0; i < 40; i++ {
		msgs = append(msgs, llm.Message{
			Role: "assistant",
			ToolCalls: []llm.ToolCall{{ID: "id", Type: "function",
				Function: llm.FunctionCall{Name: "read", Arguments: `{}`}}},
		})
		msgs = append(msgs, llm.Message{Role: "tool", ToolCallID: "id", Name: "read",
			Content: strings.Repeat("x", 500)})
	}
	// End with a trivial user so the kept window does not include the real task.
	msgs = append(msgs, llm.Message{Role: "user", Content: "continue"})
	out := CompactOpts(msgs, 4_000, "", 2_000)
	var anchor, stub string
	for _, m := range out {
		if m.Role != "user" {
			continue
		}
		if strings.Contains(m.Content, "[task anchors") {
			anchor = m.Content
		}
		if strings.Contains(m.Content, "context compacted") {
			stub = m.Content
		}
	}
	if anchor == "" {
		t.Fatalf("missing task anchor in %#v", out)
	}
	if !strings.Contains(anchor, "line_hash bug") || !strings.Contains(anchor, "tui.go") {
		t.Fatalf("anchor lost original task: %q", anchor)
	}
	if strings.Contains(anchor, "\n1. hi\n") || strings.HasPrefix(strings.TrimSpace(strings.Split(anchor, "\n")[0]), "1. hi") {
		// "hi" is trivial and must not be the sole anchor when a real task exists
	}
	if !strings.Contains(anchor, "line_hash") {
		t.Fatalf("want real task not only hi: %q", anchor)
	}
	if stub == "" {
		t.Fatalf("missing compact stub")
	}
	if !strings.Contains(strings.ToLower(stub), "do not ask the user to restate") {
		t.Fatalf("stub should discourage restate: %q", stub)
	}
}

func TestContextCharsBudgetScalesWithWindow(t *testing.T) {
	if got := ContextCharsBudget(0, 0); got != 0 {
		t.Fatalf("unknown window → %d", got)
	}
	// Default 0.75 ratio: 1M × 4 × 0.75 = 3M chars, hard-capped at 1.6M (~400k tok-eq).
	got := ContextCharsBudget(1_000_000, 0)
	if got != 1_600_000 {
		t.Fatalf("1M @0.75 default: got %d want 1600000", got)
	}
	// Explicit lower ratio under the cap passes through.
	if got := ContextCharsBudget(1_000_000, 0.3); got != 1_200_000 {
		t.Fatalf("1M @0.3: got %d", got)
	}
	// Above the cap is clamped regardless of ratio.
	if got := ContextCharsBudget(1_000_000, 0.55); got != 1_600_000 {
		t.Fatalf("1M @0.55: got %d want 1600000 (hard cap)", got)
	}
	// Smaller window still usable.
	if got := ContextCharsBudget(128_000, DefaultCompactRatio); got < 80_000 {
		t.Fatalf("128k budget %d", got)
	}
	if ClampCompactRatio(0) != DefaultCompactRatio {
		t.Fatal("zero ratio → default")
	}
	if ClampCompactRatio(0.1) != 0.3 || ClampCompactRatio(0.99) != 0.95 {
		t.Fatal("clamp bounds")
	}
}

func TestCompactPreservesTaskAndTools(t *testing.T) {
	msgs := []llm.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "Implement the payment refund API with idempotency keys"},
		{Role: "assistant", Content: "I'll explore", ToolCalls: []llm.ToolCall{{
			ID: "1", Type: "function",
			Function: llm.FunctionCall{Name: "grep", Arguments: `{"pattern":"refund"}`},
		}}},
		{Role: "tool", ToolCallID: "1", Name: "grep", Content: strings.Repeat("hit\n", 500)},
		{Role: "assistant", Content: "found handlers"},
	}
	// Fill with noise so compact drops the middle.
	for i := 0; i < 30; i++ {
		msgs = append(msgs, llm.Message{Role: "user", Content: "ok continue"})
		msgs = append(msgs, llm.Message{Role: "assistant", Content: strings.Repeat("x", 200)})
	}
	msgs = append(msgs, llm.Message{Role: "user", Content: "ship it"})

	out := CompactOpts(msgs, 4_000, "", 2_000)
	var joined strings.Builder
	for _, m := range out {
		joined.WriteString(m.Content)
		joined.WriteByte('\n')
	}
	s := joined.String()
	if !strings.Contains(s, "payment refund") && !strings.Contains(s, "idempotency") {
		t.Fatalf("task pin lost:\n%s", s)
	}
	// Default stub should mention tools from dropped span when present.
	if !strings.Contains(s, "grep") && !strings.Contains(s, "task anchors") {
		t.Fatalf("expected tool or anchor signal:\n%s", s)
	}
}

// TestCompactSnippet locks the whitespace-collapsing + truncation semantics of
// compactSnippet after it was rewritten to stop scanning at the rune budget.
func TestCompactSnippet(t *testing.T) {
	tests := []struct {
		name     string
		in       string
		maxRunes int
		want     string
	}{
		{"empty", "", 10, ""},
		{"only spaces", "   \t\n ", 10, ""},
		{"collapse runs", "  a \t\n b   c  ", 10, "a b c"},
		{"exact budget", "abcde", 5, "abcde"},
		{"truncate", "abcdefgh", 5, "abcd…"},
		{"truncate collapsed", "aa  bb  cc  dd", 6, "aa bb…"},
		{"no budget keeps all", strings.Repeat("ab ", 5), 0, "ab ab ab ab ab"},
		{"multibyte truncate", "héllo wörld", 4, "hél…"},
		{"multibyte fits", "héllo", 5, "héllo"},
		{"unicode space collapses", "a\u00a0\u2003b", 10, "a b"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := compactSnippet(tc.in, tc.maxRunes); got != tc.want {
				t.Fatalf("compactSnippet(%q, %d) = %q, want %q", tc.in, tc.maxRunes, got, tc.want)
			}
		})
	}
}

// Long input must not be normalized past the budget (perf invariant): the
// result length stays bounded regardless of input size.
func TestCompactSnippetLongInputBounded(t *testing.T) {
	in := strings.Repeat("word ", 100_000)
	got := compactSnippet(in, 120)
	if n := len([]rune(got)); n != 120 {
		t.Fatalf("rune len = %d, want 120", n)
	}
	if !strings.HasSuffix(got, "…") {
		t.Fatalf("want ellipsis suffix, got %q", got)
	}
}

func TestCompactTieredSnipSuffices(t *testing.T) {
	msgs := []llm.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "inspect"},
		{Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "1", Type: "function", Function: llm.FunctionCall{Name: "read", Arguments: `{}`}}}},
		{Role: "tool", ToolCallID: "1", Name: "read", Content: strings.Repeat("x", 12_000)},
		{Role: "user", Content: "continue"},
	}
	got := CompactTiered(msgs, 4_000, "", 24_000)
	if got.Layer != "snip" || got.CharsSaved <= 0 {
		t.Fatalf("result=%+v", got)
	}
	if len(got.Messages) != len(msgs) {
		t.Fatalf("snip dropped messages: %d -> %d", len(msgs), len(got.Messages))
	}
	if !strings.Contains(got.Messages[3].Content, "…(snip)") {
		t.Fatalf("missing snip marker: %q", got.Messages[3].Content)
	}
	if len(msgs[3].Content) != 12_000 {
		t.Fatal("input mutated")
	}
}

// Policy tool cap applies even when total history is already under the context
// target — otherwise oversized tool bodies survive "under budget" snips forever.
func TestCompactTieredAppliesPolicyCapUnderBudget(t *testing.T) {
	msgs := []llm.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "inspect"},
		{Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "1", Type: "function", Function: llm.FunctionCall{Name: "read", Arguments: `{}`}}}},
		{Role: "tool", ToolCallID: "1", Name: "read", Content: strings.Repeat("z", 80_000)},
	}
	// Target well above EstChars so need <= 0; policy must still cap the tool.
	got := CompactTiered(msgs, 500_000, "", 5_000)
	if got.Layer != "snip" {
		t.Fatalf("layer=%q want snip", got.Layer)
	}
	if n := len(got.Messages[3].Content); n > 6_000 {
		t.Fatalf("policy cap not applied under budget: tool len=%d", n)
	}
	if !strings.Contains(got.Messages[3].Content, "…(snip)") {
		t.Fatalf("missing snip marker under budget: %q", got.Messages[3].Content[:min(40, len(got.Messages[3].Content))])
	}
}

func TestCompactTieredEscalatesWithoutOrphans(t *testing.T) {
	msgs := []llm.Message{{Role: "system", Content: "sys"}, {Role: "user", Content: "finish the task"}}
	for i := 0; i < 35; i++ {
		id := fmt.Sprintf("call-%d", i)
		msgs = append(msgs,
			llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{{ID: id, Type: "function", Function: llm.FunctionCall{Name: "read", Arguments: `{}`}}}},
			llm.Message{Role: "tool", ToolCallID: id, Name: "read", Content: strings.Repeat("x", 900)},
		)
	}
	got := CompactTiered(msgs, 3_000, "", 24_000)
	if got.Layer != "drop" || got.CharsSaved <= 0 {
		t.Fatalf("result layer=%q saved=%d", got.Layer, got.CharsSaved)
	}
	calls := map[string]bool{}
	for _, m := range got.Messages {
		if m.Role == "assistant" {
			for _, tc := range m.ToolCalls {
				calls[tc.ID] = true
			}
		}
		if m.Role == "tool" && !calls[m.ToolCallID] {
			t.Fatalf("orphan tool result %q", m.ToolCallID)
		}
	}
}

func TestApplyCompactReportsLayerAndSavings(t *testing.T) {
	msgs := []llm.Message{{Role: "system", Content: "sys"}, {Role: "user", Content: "inspect"},
		{Role: "tool", Name: "read", Content: strings.Repeat("z", 10_000)}}
	var event AfterCompactEvent
	opt := Options{MaxContextChars: 4_000, MaxToolResultChars: 24_000,
		Hooks: Hooks{AfterCompact: []AfterCompactFunc{func(_ context.Context, e AfterCompactEvent) { event = e }}}}
	if _, _, err := applyCompact(context.Background(), msgs, opt, newRatioCalibrator()); err != nil {
		t.Fatal(err)
	}
	if event.Layer != "snip" || event.CharsSaved <= 0 {
		t.Fatalf("event=%+v", event)
	}
}

func TestCompactTieredSmallOveragePreservesToolContext(t *testing.T) {
	tool := strings.Repeat("x", 24_000)
	msgs := []llm.Message{{Role: "system", Content: "sys"}, {Role: "user", Content: "inspect"},
		{Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "1", Type: "function", Function: llm.FunctionCall{Name: "read", Arguments: `{}`}}}},
		{Role: "tool", ToolCallID: "1", Name: "read", Content: tool}}
	before := EstChars(msgs)
	need := 2_000
	got := CompactTiered(msgs, before-need, "", DefaultMaxToolResultChars)
	if got.Layer != "snip" || got.CharsSaved < need {
		t.Fatalf("result=%+v", got)
	}
	if n := len(got.Messages[3].Content); n < 20_000 || n <= minSnippedToolChars {
		t.Fatalf("small overage gutted tool result to %d bytes", n)
	}
}

func TestCompactTieredReportsOverBudget(t *testing.T) {
	msgs := []llm.Message{{Role: "system", Content: strings.Repeat("s", 8_000)},
		{Role: "user", Content: strings.Repeat("p", 20_000)}}
	got := CompactTiered(msgs, 100, "", DefaultMaxToolResultChars)
	if !got.OverBudget || got.CharsAfter <= 100 {
		t.Fatalf("result=%+v", got)
	}
}

func TestCompactStubRecordsDroppedWork(t *testing.T) {
	dropped := []llm.Message{
		{Role: "user", Content: "original task: wire the parser"},
		{Role: "assistant", Content: "on it"},
	}
	kept := []llm.Message{{Role: "user", Content: "continue"}}
	got := defaultCompactStub(dropped, kept, nil)
	if !strings.Contains(got, "compacted") || !strings.Contains(got, "Dropped 2 messages") {
		t.Fatalf("stub missing drop record:\n%s", got)
	}
}
