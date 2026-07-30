package agent

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/subosito/mow/internal/llm"
)

// Default tool-result size when Options.MaxToolResultChars is unset.
const DefaultMaxToolResultChars = 24_000

// DefaultMaxContextChars is the config default (~25–30k tokens). When the
// gateway publishes a larger context_window, Engine scales the soft budget up
// from this floor instead of compacting early on 1M-token models.
const DefaultMaxContextChars = 100_000

// DefaultCompactRatio is the fraction of gateway context_window used as the
// soft history budget when auto-scaling (1M window → ~800k tokens of history
// at ~4 chars/token before compaction).
const DefaultCompactRatio = 0.8

// ClampCompactRatio bounds ratio for auto budget. Non-positive → default.
func ClampCompactRatio(ratio float64) float64 {
	if ratio <= 0 {
		return DefaultCompactRatio
	}
	if ratio < 0.3 {
		return 0.3
	}
	if ratio > 0.95 {
		return 0.95
	}
	return ratio
}

// ContextCharsBudget converts a gateway context_window (tokens) into a soft
// history char budget. ~4 chars/token × ratio of the window (default 0.8 so
// system, tools, and reply keep headroom). Returns 0 when window is unknown.
func ContextCharsBudget(windowTokens int, ratio float64) int {
	if windowTokens <= 0 {
		return 0
	}
	ratio = ClampCompactRatio(ratio)
	b := int(float64(windowTokens) * 4 * ratio)
	const floor = 80_000
	const ceil = 3_500_000 // ~875k tokens of history — memory/safety cap
	if b < floor {
		return floor
	}
	if b > ceil {
		return ceil
	}
	return b
}

// CompactOpts reduces message history to roughly maxChars while keeping system
// and the most recent turns; maxToolChars is the per-tool-result char budget.
// Older middle content is replaced with a task-preserving anchor + summary.
// Soft overflow guard (char estimate), not token-accurate.
//
// Strategy:
//  1. Trim tool bodies.
//  2. If still over: keep system + pinned user intents + stub + last keepLast.
//  3. Further trim tools if needed.
func CompactOpts(messages []llm.Message, maxChars int, summary string, maxToolChars int) []llm.Message {
	if maxChars <= 0 || estChars(messages) <= maxChars {
		return trimAllToolResults(messages, maxToolChars, maxToolChars/2)
	}
	if maxToolChars <= 0 {
		maxToolChars = DefaultMaxToolResultChars
	}
	if len(messages) <= 3 {
		return trimAllToolResults(messages, maxToolChars, maxToolChars/2)
	}

	msgs := trimAllToolResults(messages, maxToolChars, maxToolChars/2)
	if estChars(msgs) <= maxChars {
		return msgs
	}

	keepLast := keepLastForBudget(maxChars)
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
	kept = alignKeepAtUser(kept)

	// Pin user intents from the full conversation (not only dropped) so a
	// trailing "hi" or thrash window cannot erase the real task.
	pins := collectUserPins(rest, kept, maxChars)

	stub := strings.TrimSpace(summary)
	if stub == "" {
		stub = defaultCompactStub(dropped, kept, pins)
	}

	out := append([]llm.Message{}, system...)
	if len(pins) > 0 {
		out = append(out, llm.Message{
			Role:    "user",
			Content: formatTaskAnchor(pins),
		})
	}
	out = append(out, llm.Message{Role: "user", Content: stub})
	out = append(out, kept...)

	if estChars(out) > maxChars {
		out = trimAllToolResults(out, maxToolChars/3, 800)
	}
	if estChars(out) > maxChars {
		out = trimAllToolResults(out, 800, 400)
	}
	// Last resort: shrink pin/stub bodies (never drop the anchor entirely).
	if estChars(out) > maxChars {
		out = shrinkAnchors(out, maxChars)
	}
	return out
}

func keepLastForBudget(maxChars int) int {
	switch {
	case maxChars >= 1_500_000:
		return 64
	case maxChars >= 500_000:
		return 40
	case maxChars >= 200_000:
		return 28
	default:
		return 20
	}
}

// collectUserPins gathers substantive user messages to preserve across compact.
// Skips pure noise ("hi", "ok") when longer intents exist; always keeps at least
// the first user turn if nothing else qualifies. Larger budgets keep more/longer pins.
func collectUserPins(all, kept []llm.Message, maxChars int) []string {
	maxPins, snipLen := pinBudget(maxChars)
	keptSet := map[string]bool{}
	for _, m := range kept {
		if m.Role == "user" {
			keptSet[compactSnippet(m.Content, 120)] = true
		}
	}
	var pins []string
	var first string
	for _, m := range all {
		if m.Role != "user" {
			continue
		}
		// Skip compaction machinery / anchors from prior rounds.
		if strings.Contains(m.Content, "[context compacted") ||
			strings.Contains(m.Content, "[task anchors") {
			continue
		}
		snip := compactSnippet(m.Content, snipLen)
		if snip == "" {
			continue
		}
		if first == "" {
			first = snip
		}
		if isTrivialUser(snip) {
			continue
		}
		// Skip if this intent already lives in the kept window.
		if keptSet[compactSnippet(snip, 120)] {
			continue
		}
		// Dedupe exact pins.
		dup := false
		for _, p := range pins {
			if p == snip {
				dup = true
				break
			}
		}
		if dup {
			continue
		}
		pins = append(pins, snip)
		if len(pins) >= maxPins {
			break
		}
	}
	if len(pins) == 0 && first != "" && !keptSet[compactSnippet(first, 120)] {
		pins = []string{first}
	}
	return pins
}

// pinBudget scales how many / how long user intents we preserve with window size.
func pinBudget(maxChars int) (maxPins, snipLen int) {
	switch {
	case maxChars >= 1_500_000:
		return 16, 1_200
	case maxChars >= 500_000:
		return 12, 900
	default:
		return 8, 600
	}
}

func isTrivialUser(s string) bool {
	s = strings.ToLower(strings.TrimSpace(s))
	switch s {
	case "", "hi", "hello", "hey", "ok", "okay", "yes", "no", "thanks", "thank you", "continue", "go on", "y", "n":
		return true
	}
	// Very short non-task pings
	if utf8.RuneCountInString(s) < 12 && !strings.ContainsAny(s, "/\\.") {
		return true
	}
	return false
}

func formatTaskAnchor(pins []string) string {
	var b strings.Builder
	b.WriteString("[task anchors — preserved across context compaction]\n")
	b.WriteString("These are the user's requests so far. Continue this work; do not invent a new task.\n")
	for i, p := range pins {
		fmt.Fprintf(&b, "\n%d. %s\n", i+1, p)
	}
	return strings.TrimSpace(b.String())
}

// defaultCompactStub builds a short note when no PreCompact summary is supplied.
// It records what was dropped and which tools ran so work is not silently erased.
func defaultCompactStub(dropped, kept []llm.Message, pins []string) string {
	var nUser, nAsst, nTool int
	for _, m := range dropped {
		switch m.Role {
		case "user":
			nUser++
		case "assistant":
			nAsst++
		case "tool":
			nTool++
		}
	}
	var b strings.Builder
	b.WriteString("[context compacted to fit the model window]\n")
	fmt.Fprintf(&b, "Dropped %d messages (%d user, %d assistant, %d tool); older tool bodies trimmed.\n",
		len(dropped), nUser, nAsst, nTool)
	if tools := toolsUsed(dropped); len(tools) > 0 {
		show := tools
		if len(show) > 24 {
			show = show[:24]
		}
		fmt.Fprintf(&b, "Tools used in dropped span: %s.\n", strings.Join(show, ", "))
	}
	b.WriteString("Continue the same task using the task anchors above (if any) and the live turns below.\n")
	b.WriteString("Do not ask the user to restate the task unless anchors and live context are empty or contradictory.\n")
	if len(pins) == 0 {
		// Fallback: snag something from dropped users (may include trivial).
		var users []string
		for _, m := range dropped {
			if m.Role != "user" {
				continue
			}
			if t := compactSnippet(m.Content, 400); t != "" && !strings.Contains(t, "[context compacted") {
				users = append(users, t)
			}
		}
		if len(users) > 0 {
			b.WriteString("\nDropped user messages (fallback — no non-trivial anchors found):\n")
			show := users
			if len(show) > 4 {
				show = append([]string{users[0]}, users[len(users)-2:]...)
			}
			for i, u := range show {
				fmt.Fprintf(&b, "%d. %s\n", i+1, u)
			}
		}
	}
	return strings.TrimSpace(b.String())
}

// toolsUsed returns distinct tool names seen in assistant tool_calls / tool msgs.
func toolsUsed(msgs []llm.Message) []string {
	seen := make(map[string]bool)
	var names []string
	for _, m := range msgs {
		if m.Role == "assistant" {
			for _, tc := range m.ToolCalls {
				n := strings.TrimSpace(tc.Function.Name)
				if n == "" || seen[n] {
					continue
				}
				seen[n] = true
				names = append(names, n)
			}
		}
		if m.Role == "tool" {
			n := strings.TrimSpace(m.Name)
			if n == "" || seen[n] {
				continue
			}
			seen[n] = true
			names = append(names, n)
		}
	}
	return names
}

func shrinkAnchors(msgs []llm.Message, maxChars int) []llm.Message {
	out := append([]llm.Message(nil), msgs...)
	for i := range out {
		if out[i].Role != "user" {
			continue
		}
		if strings.Contains(out[i].Content, "[task anchors") || strings.Contains(out[i].Content, "[context compacted") {
			out[i].Content = compactSnippet(out[i].Content, 1_200)
		}
	}
	if estChars(out) > maxChars {
		// Drop oldest non-system, non-anchor tool-heavy kept? leave as-is — better over budget than empty task.
	}
	return out
}

// compactSnippet collapses whitespace and truncates for the compact stub.
func compactSnippet(s string, maxRunes int) string {
	s = strings.Join(strings.Fields(s), " ")
	if s == "" {
		return ""
	}
	if maxRunes <= 0 || utf8.RuneCountInString(s) <= maxRunes {
		return s
	}
	r := []rune(s)
	return string(r[:maxRunes-1]) + "…"
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
