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
	lower := strings.ToLower(s)
	for _, p := range thinkTagPairs {
		i := strings.Index(lower, strings.ToLower(p.open))
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

func indexCloseTag(s, closeTag string) int {
	if closeTag == "```" {
		// Fence close: first ``` that starts a line or appears after content.
		return strings.Index(s, "```")
	}
	return strings.Index(strings.ToLower(s), strings.ToLower(closeTag))
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
