package agent

import (
	"fmt"

	"github.com/subosito/mow/internal/llm"
)

// Default tool-result size when Options.MaxToolResultChars is unset.
const DefaultMaxToolResultChars = 24_000

// CompactOpts reduces message history to roughly maxChars while keeping system
// and the most recent turns; maxToolChars is the per-tool-result char budget.
// Older middle content is replaced with a short summary stub. This is a soft
// overflow guard, not token-accurate.
// If summary is non-empty it is used as the stub content instead of the default.
//
// Strategy (token-lean):
//  1. Trim all tool bodies to maxToolChars (recent slightly larger budget).
//  2. If still over maxChars, drop the middle of history (keep system + last keepLast).
//  3. If still over, aggressively shrink older tool results again.
func CompactOpts(messages []llm.Message, maxChars int, summary string, maxToolChars int) []llm.Message {
	if maxChars <= 0 || estChars(messages) <= maxChars {
		// Still proactively trim oversized tool results so one bash dump cannot
		// dominate even under the budget.
		return trimAllToolResults(messages, maxToolChars, maxToolChars/2)
	}
	if maxToolChars <= 0 {
		maxToolChars = DefaultMaxToolResultChars
	}
	if len(messages) <= 3 {
		return trimAllToolResults(messages, maxToolChars, maxToolChars/2)
	}

	// First pass: cap tool payloads (recent tools get full budget; older get half).
	msgs := trimAllToolResults(messages, maxToolChars, maxToolChars/2)
	if estChars(msgs) <= maxChars {
		return msgs
	}

	// Keep system (if any) + last keepLast messages; drop middle.
	// keepLast is large enough for a few tool rounds (assistant + tools + user).
	keepLast := 12
	if keepLast >= len(msgs) {
		keepLast = len(msgs) - 1
	}
	var system []llm.Message
	rest := msgs
	if msgs[0].Role == "system" {
		system = msgs[:1]
		rest = msgs[1:]
	}
	if len(rest) <= keepLast {
		return trimAllToolResults(msgs, maxToolChars/2, maxToolChars/4)
	}
	dropped := rest[:len(rest)-keepLast]
	kept := rest[len(rest)-keepLast:]
	// Prefer cutting on a user boundary so we do not orphan tool_results.
	kept = alignKeepAtUser(kept)

	stub := summary
	if stub == "" {
		stub = fmt.Sprintf("[context compacted: dropped %d earlier messages to fit limit]", len(dropped))
	}
	summaryMsg := llm.Message{
		Role:    "user",
		Content: stub,
	}
	out := append([]llm.Message{}, system...)
	out = append(out, summaryMsg)
	out = append(out, kept...)

	// Second pass: shrink tool bodies further if still over budget.
	if estChars(out) > maxChars {
		out = trimAllToolResults(out, maxToolChars/3, 800)
	}
	// Last resort: hard-cap every tool body.
	if estChars(out) > maxChars {
		out = trimAllToolResults(out, 800, 400)
	}
	return out
}

// alignKeepAtUser drops leading non-user messages so the kept window starts at
// a user turn. In a tool-heavy run no user message may survive the window; then
// at least drop leading tool results whose assistant tool_use was cut — an
// orphan tool_result is rejected by both wires (HTTP 400).
func alignKeepAtUser(kept []llm.Message) []llm.Message {
	for i, m := range kept {
		if m.Role == "user" {
			return kept[i:]
		}
	}
	for i, m := range kept {
		if m.Role != "tool" {
			return kept[i:]
		}
	}
	return nil
}

// trimAllToolResults returns a copy with tool message contents truncated.
// recentMax applies to the last half of messages; olderMax to the rest.
func trimAllToolResults(messages []llm.Message, recentMax, olderMax int) []llm.Message {
	if recentMax <= 0 {
		recentMax = DefaultMaxToolResultChars
	}
	if olderMax <= 0 {
		olderMax = recentMax / 2
	}
	out := append([]llm.Message(nil), messages...)
	cutoff := len(out) / 2
	for i := range out {
		if out[i].Role != "tool" {
			continue
		}
		lim := olderMax
		if i >= cutoff {
			lim = recentMax
		}
		out[i].Content = TruncateToolResult(out[i].Content, lim)
	}
	return out
}

// TruncateToolResult shortens a tool result for model history.
func TruncateToolResult(s string, maxChars int) string {
	if maxChars <= 0 || len(s) <= maxChars {
		return s
	}
	// Prefer cutting at a newline near the limit.
	cut := maxChars
	if i := lastIndexByte(s[:maxChars], '\n'); i > maxChars*3/4 {
		cut = i
	}
	return s[:cut] + "\n…(truncated)"
}

func lastIndexByte(s string, c byte) int {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == c {
			return i
		}
	}
	return -1
}

func estChars(msgs []llm.Message) int {
	n := 0
	for _, m := range msgs {
		n += len(m.Content) + len(m.Role) + 8
		for _, tc := range m.ToolCalls {
			n += len(tc.Function.Name) + len(tc.Function.Arguments)
		}
	}
	return n
}
