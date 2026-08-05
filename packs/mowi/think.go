package mowi

import (
	"github.com/subosito/mow"
)

// Thinking-tag handling lives in mow now (internal/agent/think.go): the agent
// loop strips inline CoT from committed turns, so history, sessions, and
// Result.Text are always tag-free. mowi keeps only what a live view needs —
// incremental extraction over the accumulating stream buffer.

// extractThinking delegates to mow (dialect table lives there).
func extractThinking(s string) (visible, thinking string, unclosed bool) {
	return mow.ExtractThinking(s)
}

// stripThinkingContent is extractThinking for a finished answer (trim outer junk).
func stripThinkingContent(s string) (visible, thinking string) {
	return mow.StripThinking(s)
}
