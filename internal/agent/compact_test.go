package agent

import (
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
	if estChars(out) >= estChars(msgs) {
		t.Fatalf("expected smaller: in=%d out=%d", estChars(msgs), estChars(out))
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
	if estChars(out) > 8_000 {
		t.Fatalf("still too large: %d", estChars(out))
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
