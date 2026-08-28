package agent

import (
	"cmp"
	"fmt"
	"slices"
	"strings"
	"unicode"
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
// soft history budget when auto-scaling (1M window → ~750k tokens of history
// at ~4 chars/token before the hard cap). 0.75: first compact around three
// quarters of the window, then resume toward 70% of that budget. Still
// below a typical ~85% auto-compact trip. See MaxContextCharsHardCap.
const DefaultCompactRatio = 0.75

// MaxContextCharsHardCap is the absolute ceiling on the soft history budget
// (in chars, ~400k tokens at ~4 chars/token), enforced in applyCompact
// regardless of gateway context_window or configured ratio. A huge window
// must not let history grow past ~40% of a 1M-token model before compacting —
// oversized histories are the dominant token-waste failure mode in long
// coding sessions (contexts of 500K+ tokens cost tens of millions of input
// tokens before the first compaction).
const MaxContextCharsHardCap = 1_600_000

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
// history char budget. ~4 chars/token × ratio of the window (default 0.5 so
// system, tools, and reply keep headroom), floored at 80k and hard-capped at
// MaxContextCharsHardCap so a huge window cannot inflate history past the
// ceiling. Returns 0 when window is unknown.
func ContextCharsBudget(windowTokens int, ratio float64) int {
	if windowTokens <= 0 {
		return 0
	}
	ratio = ClampCompactRatio(ratio)
	b := int(float64(windowTokens) * 4 * ratio)
	const floor = 80_000
	if b < floor {
		return floor
	}
	if b > MaxContextCharsHardCap {
		return MaxContextCharsHardCap
	}
	return b
}

// Chars-per-token calibration. MaxContextChars is a char budget authored
// against the classic ~4 chars/token heuristic. Real ratios vary a lot: dense
// code / JSON tool output tokenizes near ~2.5 chars/token, English prose near
// ~5–6. We observe provider-reported input tokens against the chars we actually
// sent and smooth the result, so the pre-call budget check tracks the real
// window instead of a fixed guess. Config keys are unchanged.
const (
	defaultCharsPerToken = 4.0
	minCharsPerToken     = 2.0
	maxCharsPerToken     = 8.0
	// ratioAlpha is the EWMA weight of a new sample (low = stable, high = jumpy).
	ratioAlpha = 0.3
)

// ratioCalibrator maintains a smoothed chars/token estimate. Zero value is not
// usable; use newRatioCalibrator. Not safe for concurrent use (loop-owned).
type ratioCalibrator struct {
	ratio   float64
	samples int
}

func newRatioCalibrator() *ratioCalibrator {
	return &ratioCalibrator{ratio: defaultCharsPerToken}
}

// Ratio returns the current smoothed chars/token estimate, always in
// [minCharsPerToken, maxCharsPerToken]; seeded at defaultCharsPerToken.
func (c *ratioCalibrator) Ratio() float64 {
	if c == nil || c.ratio <= 0 {
		return defaultCharsPerToken
	}
	return clampRatio(c.ratio)
}

// Observe records one call: chars sent to the provider vs the input tokens the
// provider billed. Non-positive or implausible samples are ignored so a
// provider that omits usage (or reports cached-only tokens) cannot poison the
// estimate; the seed then stays in effect.
func (c *ratioCalibrator) Observe(chars, inputTokens int) {
	if c == nil || chars <= 0 || inputTokens <= 0 {
		return
	}
	sample := clampRatio(float64(chars) / float64(inputTokens))
	c.ratio = c.ratio + ratioAlpha*(sample-c.ratio)
	c.samples++
}

func clampRatio(r float64) float64 {
	if r < minCharsPerToken {
		return minCharsPerToken
	}
	if r > maxCharsPerToken {
		return maxCharsPerToken
	}
	return r
}

// budgetChars rescales a raw char count into "budget chars" — the char count
// the same text would have at the ~4 chars/token heuristic MaxContextChars was
// written against. Code-heavy history (low ratio) inflates, prose deflates.
func budgetChars(chars int, ratio float64) int {
	if chars <= 0 {
		return 0
	}
	if ratio <= 0 {
		ratio = defaultCharsPerToken
	}
	return int(float64(chars) * defaultCharsPerToken / clampRatio(ratio))
}

// CompactResumeRatio is how far below the trigger we try to land. The soft
// budget is when to fire; this is the floor we aim for so the next tool
// batch has headroom instead of immediately re-tripping.
const CompactResumeRatio = 0.7

// CompactResumeBudget is the char target after a compact that just tripped
// `budget`. Always strictly below budget when budget > 1.
func CompactResumeBudget(budget int) int {
	if budget <= 0 {
		return budget
	}
	t := int(float64(budget) * CompactResumeRatio)
	if t < 1 {
		t = 1
	}
	if budget > 1 && t >= budget {
		return budget - 1
	}
	return t
}

// CompactTarget converts the configured char budget into the raw char budget
// CompactTiered should trim to, given the calibrated ratio (inverse of
// budgetChars, so compaction stops exactly at the scaled limit).
func CompactTarget(maxChars int, ratio float64) int {
	if maxChars <= 0 {
		return maxChars
	}
	if ratio <= 0 {
		ratio = defaultCharsPerToken
	}
	t := int(float64(maxChars) * clampRatio(ratio) / defaultCharsPerToken)
	if t < 1 {
		t = 1
	}
	return t
}

// CompactResult describes which cheap-first layer was required. The input is
// never mutated; Messages is a projection for the next provider call only.
// Projections share ToolCalls slices with live history; layers treat them read-only.
type CompactResult struct {
	Messages       []llm.Message
	Layer          string // "snip" or "drop"
	CharsBefore    int
	CharsAfter     int
	MessagesBefore int
	MessagesAfter  int
	OverBudget     bool
	CharsSaved     int
}

// CompactTiered first snips the longest tool results. Only when that cannot
// reach maxChars does it replace complete older turn ranges with task anchors
// and a summary. Raw session history is deliberately outside this function.
func CompactTiered(messages []llm.Message, maxChars int, summary string, maxToolChars int) CompactResult {
	if maxToolChars <= 0 {
		maxToolChars = DefaultMaxToolResultChars
	}
	snipped := snipLongestToolResults(messages, maxChars, maxToolChars)
	if maxChars <= 0 || EstChars(snipped) <= maxChars {
		return compactResult(messages, snipped, "snip", maxChars)
	}
	out := CompactOpts(snipped, maxChars, summary, maxToolChars)
	return compactResult(messages, out, "drop", maxChars)
}

func compactResult(before, after []llm.Message, layer string, target int) CompactResult {
	beforeChars, afterChars := EstChars(before), EstChars(after)
	return CompactResult{
		Messages: after, Layer: layer,
		CharsBefore: beforeChars, CharsAfter: afterChars,
		MessagesBefore: len(before), MessagesAfter: len(after),
		OverBudget: target > 0 && afterChars > target,
		CharsSaved: max(0, beforeChars-afterChars),
	}
}

const minSnippedToolChars = 800
const snipMarker = "\n…(snip)"

// snipLongestToolResults makes a copy and reduces tool bodies longest-first.
// It preserves every message and therefore cannot orphan a tool call/result.
func snipLongestToolResults(messages []llm.Message, target, maxToolChars int) []llm.Message {
	out := append([]llm.Message(nil), messages...)
	type candidate struct{ index, size int }
	var candidates []candidate
	for i := range out {
		if out[i].Role == "tool" && len(out[i].Content) > minSnippedToolChars {
			candidates = append(candidates, candidate{i, len(out[i].Content)})
		}
	}
	slices.SortStableFunc(candidates, func(a, b candidate) int { return cmp.Compare(b.size, a.size) })
	need := EstChars(out) - target
	for _, c := range candidates {
		limit := maxToolChars
		if limit < minSnippedToolChars {
			limit = minSnippedToolChars
		}
		// Always enforce the per-result policy cap on oversized tools (even when
		// the total is already under the context target). Then, only if we still
		// need more room, snip further — but never below minSnippedToolChars.
		// Taking the *larger* of (policy cap, need-driven size) when both apply
		// would refuse to snip below the policy cap when need is large; instead
		// policy is the ceiling and need can only go lower.
		want := c.size
		if c.size > limit {
			want = limit
		}
		if need > 0 {
			needCap := max(minSnippedToolChars, c.size-need)
			if needCap < want {
				want = needCap
			}
		}
		if want >= c.size {
			continue
		}
		out[c.index].Content = snipToolResult(out[c.index].Content, want)
		need -= c.size - len(out[c.index].Content)
	}
	return out
}

func snipToolResult(s string, maxChars int) string {
	if maxChars <= 0 || len(s) <= maxChars {
		return s
	}
	keep := maxChars - len(snipMarker)
	if keep < 0 {
		keep = 0
	}
	return s[:keep] + snipMarker
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
	if maxChars <= 0 || EstChars(messages) <= maxChars {
		return trimAllToolResults(messages, maxToolChars, maxToolChars/2)
	}
	if maxToolChars <= 0 {
		maxToolChars = DefaultMaxToolResultChars
	}
	if len(messages) <= 3 {
		return trimAllToolResults(messages, maxToolChars, maxToolChars/2)
	}

	msgs := trimAllToolResults(messages, maxToolChars, maxToolChars/2)
	if EstChars(msgs) <= maxChars {
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

	// Shrink the kept window until under budget (or keepLast is minimal). A
	// fixed keepLast left large non-tool histories OverBudget with no further
	// reduction — manual /compact and auto drop then looked like no-ops.
	var out []llm.Message
	for {
		if keepLast < 2 {
			keepLast = 2
		}
		if keepLast >= len(rest) {
			keepLast = len(rest) - 1
		}
		if keepLast < 1 {
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

		out = append([]llm.Message{}, system...)
		if len(pins) > 0 {
			out = append(out, llm.Message{
				Role:    "user",
				Content: formatTaskAnchor(pins),
			})
		}
		out = append(out, llm.Message{Role: "user", Content: stub})
		out = append(out, kept...)

		if EstChars(out) > maxChars {
			out = trimAllToolResults(out, maxToolChars/3, 800)
		}
		if EstChars(out) > maxChars {
			out = trimAllToolResults(out, 800, 400)
		}
		// Last resort: shrink pin/stub bodies (never drop the anchor entirely).
		if EstChars(out) > maxChars {
			out = shrinkAnchors(out, maxChars)
		}
		if EstChars(out) <= maxChars || keepLast <= 2 {
			break
		}
		// Drop more of the recent window and rebuild.
		next := keepLast / 2
		if next >= keepLast {
			next = keepLast - 2
		}
		if next < 2 {
			next = 2
		}
		if next >= keepLast {
			break
		}
		keepLast = next
	}
	return out
}

func keepLastForBudget(maxChars int) int {
	// Tighter windows for smaller targets so aggressive manual /compact
	// (≈20% of non-system body) can actually land near the budget instead of
	// retaining a large fixed recent tail.
	switch {
	case maxChars >= 1_500_000:
		return 64
	case maxChars >= 500_000:
		return 40
	case maxChars >= 200_000:
		return 28
	case maxChars >= 50_000:
		return 16
	case maxChars >= 20_000:
		return 10
	case maxChars >= 8_000:
		return 6
	default:
		return 4
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
	case maxChars >= 50_000:
		return 8, 600
	case maxChars >= 20_000:
		return 4, 300
	case maxChars >= 8_000:
		return 2, 200
	default:
		return 2, 120
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
	// Progressive snip of anchor/stub bodies so aggressive /compact targets
	// can land near system+short anchors instead of stalling over budget.
	for _, lim := range []int{1_200, 400, 160} {
		for i := range out {
			if out[i].Role != "user" {
				continue
			}
			if strings.Contains(out[i].Content, "[task anchors") || strings.Contains(out[i].Content, "[context compacted") {
				out[i].Content = compactSnippet(out[i].Content, lim)
			}
		}
		if EstChars(out) <= maxChars {
			return out
		}
	}
	return out
}

// compactSnippet collapses whitespace and truncates for the compact stub.
// compactSnippet collapses whitespace runs to single spaces and truncates to
// maxRunes (last rune replaced by "…"). It stops scanning once one rune past
// the budget has been emitted: pinning reads only ~120 runes, so normalizing a
// whole multi-KB message first was the dominant allocation in CompactOpts.
func compactSnippet(s string, maxRunes int) string {
	var b strings.Builder
	if maxRunes > 0 {
		b.Grow(min(len(s), 4*(maxRunes+1)))
	} else {
		b.Grow(len(s))
	}
	runes := 0
	wrote := false
	inField := false
	truncated := false
	for _, r := range s {
		if isSpaceRune(r) {
			inField = false
			continue
		}
		if !inField {
			inField = true
			if wrote {
				b.WriteByte(' ')
				runes++
			}
		}
		b.WriteRune(r)
		wrote = true
		runes++
		// One rune past the budget is enough to know truncation is needed.
		if maxRunes > 0 && runes > maxRunes {
			truncated = true
			break
		}
	}
	if !wrote {
		return ""
	}
	out := b.String()
	if !truncated {
		return out
	}
	// Re-cut the buffered prefix at maxRunes-1 runes, then the ellipsis.
	keep := maxRunes - 1
	if keep <= 0 {
		return "…"
	}
	n := 0
	for i := range out {
		if n == keep {
			return out[:i] + "…"
		}
		n++
	}
	return out + "…"
}

func isSpaceRune(r rune) bool {
	switch r {
	case ' ', '\t', '\n', '\v', '\f', '\r', 0x85, 0xA0:
		return true
	}
	return r > 0xFF && unicode.IsSpace(r)
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

// EstChars is the raw character estimate used by compaction budgets.
func EstChars(msgs []llm.Message) int {
	n := 0
	for _, m := range msgs {
		n += len(m.Content) + len(m.Role) + 8
		for _, tc := range m.ToolCalls {
			n += len(tc.Function.Name) + len(tc.Function.Arguments)
		}
	}
	return n
}

// DefaultCharsPerToken is the classic ~4 chars/token heuristic, exported so
// callers outside the loop (Engine.Compact) can convert chars to tokens with
// the same baseline density the calibrator starts from.
const DefaultCharsPerToken = defaultCharsPerToken
