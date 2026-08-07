package agent

import (
	"strings"
)

// Inline thinking normalization. Some models emit chain-of-thought in answer
// content instead of the reasoning channel; the loop strips it so committed
// history, sessions, and Result.Text are always tag-free (UIs handle only
// live-stream display). Known wrappers, matched case-insensitively:
// Matching is case-insensitive on the open/close tags.
var thinkTagPairs = []struct{ open, close string }{
	{"<think>", "</think>"},
	{"<thinking>", "</thinking>"},
	{"<redacted_thinking>", "</redacted_thinking>"},
	{"<thought>", "</thought>"},
	{"<reasoning>", "</reasoning>"},
	{"◁think▷", "◁/think▷"},
	{"<|thinking|>", "<|/thinking|>"},
	{"<|begin_of_thought|>", "<|end_of_thought|>"},
	// Fenced CoT (closing fence is ```).
	{"```thinking", "```"},
	{"```think", "```"},
	{"```reasoning", "```"},
}

// extractThinking pulls thinking blocks out of answer text.
// Complete open/close pairs go to thinking; an unclosed open tag at the end
// (still streaming) also goes to thinking so the body never paints mid-thought.
// unclosed is true when the last open tag has no matching close yet.
func extractThinking(s string) (visible, thinking string, unclosed bool) {
	if s == "" {
		return "", "", false
	}
	var vis, think strings.Builder
	rest := s
	for rest != "" {
		openIdx, openTag, closeTag := earliestThinkOpen(rest)
		if openIdx < 0 {
			vis.WriteString(rest)
			break
		}
		vis.WriteString(rest[:openIdx])
		afterOpen := rest[openIdx+len(openTag):]
		// Drop a single leading newline after open tags / fences.
		afterOpen = strings.TrimPrefix(afterOpen, "\n")
		afterOpen = strings.TrimPrefix(afterOpen, "\r\n")
		closeIdx := indexCloseTag(afterOpen, closeTag)
		if closeIdx < 0 {
			// Still streaming thinking — hide remainder entirely.
			think.WriteString(afterOpen)
			unclosed = true
			break
		}
		think.WriteString(afterOpen[:closeIdx])
		rest = afterOpen[closeIdx+len(closeTag):]
		// Drop a single leading newline after the close tag.
		rest = strings.TrimPrefix(rest, "\n")
		rest = strings.TrimPrefix(rest, "\r\n")
		// Seam guard: stripping the block must not weld the surrounding prose
		// together ("key files.Let me"). When both sides touch with
		// non-whitespace, keep them apart with a space.
		if v := vis.String(); v != "" && rest != "" &&
			!isSpaceByte(v[len(v)-1]) && !isSpaceByte(rest[0]) {
			vis.WriteByte(' ')
		}
	}
	return vis.String(), think.String(), unclosed
}

func isSpaceByte(b byte) bool {
	switch b {
	case ' ', '\t', '\n', '\r':
		return true
	}
	return false
}

func earliestThinkOpen(s string) (idx int, open, close string) {
	idx = -1
	lower := asciiLower(s)
	for _, p := range thinkTagPairs {
		i := strings.Index(lower, asciiLower(p.open))
		if i < 0 {
			continue
		}
		if idx < 0 || i < idx {
			idx = i
			// Use actual-case slice from s for correct length (tags are ASCII).
			open = s[i : i+len(p.open)]
			close = p.close
		}
	}
	return idx, open, close
}

// asciiLower lowercases only ASCII letters, leaving every other byte untouched.
// strings.ToLower cannot be used for index-then-slice: it is Unicode-aware and
// some runes change byte length when folded (U+212A K→k shrinks 3→1, U+023A
// Ⱥ→ⱥ grows 2→3), which desynchronizes an index taken on the folded string
// from the original. Think tags are ASCII, so ASCII folding is sufficient and
// guarantees len(asciiLower(s)) == len(s), keeping every index valid on s.
func asciiLower(s string) string {
	hasUpper := false
	for i := 0; i < len(s); i++ {
		if c := s[i]; c >= 'A' && c <= 'Z' {
			hasUpper = true
			break
		}
	}
	if !hasUpper {
		return s
	}
	b := []byte(s)
	for i := 0; i < len(b); i++ {
		if c := b[i]; c >= 'A' && c <= 'Z' {
			b[i] = c + ('a' - 'A')
		}
	}
	return string(b)
}

func indexCloseTag(s, closeTag string) int {
	if closeTag == "```" {
		// Fence close: models routinely quote code inside their reasoning, so
		// the first ``` may open a nested block rather than close the CoT.
		// Walk fences in pairs — an opening fence with an info string (```go)
		// is matched to its own closing fence — and return the first fence
		// that actually terminates the thinking block.
		for i := 0; i < len(s); {
			j := strings.Index(s[i:], "```")
			if j < 0 {
				return -1
			}
			j += i
			rest := s[j+3:]
			// A bare fence (nothing but spaces before the newline) closes the
			// thinking block; one carrying an info string opens a nested block.
			line := rest
			if nl := strings.IndexByte(line, '\n'); nl >= 0 {
				line = line[:nl]
			}
			if strings.TrimSpace(line) == "" {
				return j
			}
			// Nested block: skip past its closing fence.
			k := strings.Index(rest, "```")
			if k < 0 {
				return -1
			}
			i = j + 3 + k + 3
		}
		return -1
	}
	return strings.Index(asciiLower(s), asciiLower(closeTag))
}

// stripThinkingContent is extractThinking for a finished answer (trim outer junk).
func stripThinkingContent(s string) (visible, thinking string) {
	vis, th, _ := extractThinking(s)
	return strings.TrimSpace(vis), strings.TrimSpace(th)
}

// ExtractThinking is the exported form for the root package / UIs.
func ExtractThinking(s string) (visible, thinking string, unclosed bool) {
	return extractThinking(s)
}

// StripThinking is stripThinkingContent for finished text.
func StripThinking(s string) (visible, thinking string) {
	return stripThinkingContent(s)
}
