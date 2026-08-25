package session

import (
	"fmt"
	"path/filepath"
	"strings"
)

// MaxMarkdownDigestBytes bounds the derived, greppable transcript projection.
const MaxMarkdownDigestBytes = 64 << 10
const maxDigestTurnBytes = 8 << 10

// MarkdownDigest reconstructs a bounded Markdown transcript from canonical
// JSONL. It is a pure read: callers may atomically persist the returned string,
// but this function never writes or modifies the session store.
// Host-facing: the greppable projection hosts export for review.
func (s *Store) MarkdownDigest() (string, error) {
	turns, err := s.LoadTranscript()
	if err != nil {
		return "", err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# Session %s\n\nTurns: %d\n", s.ID, len(turns))
	truncated := false
	for _, turn := range turns {
		body := strings.TrimSpace(turn.Content)
		if len(body) > maxDigestTurnBytes {
			body = body[:maxDigestTurnBytes] + "\n…(turn truncated)"
			truncated = true
		}
		part := fmt.Sprintf("\n## %s\n\n%s\n", turn.Role, body)
		if b.Len()+len(part)+32 > MaxMarkdownDigestBytes {
			truncated = true
			break
		}
		b.WriteString(part)
	}
	if truncated {
		b.WriteString("\n…(digest truncated)\n")
	}
	return b.String(), nil
}

// MarkdownDigest loads one session JSONL path and returns its derived Markdown.
func MarkdownDigest(path string) (string, error) {
	path = filepath.Clean(path)
	name := filepath.Base(path)
	id := strings.TrimSuffix(name, filepath.Ext(name))
	return (&Store{Dir: filepath.Dir(path), ID: id}).MarkdownDigest()
}
