// Package strx holds the small string helpers several packs had copied:
// first-non-empty selection and rune-safe truncation.
package strx

import (
	"strings"
	"unicode/utf8"
)

// FirstNonEmpty returns the first value that is not blank, trimmed.
func FirstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if t := strings.TrimSpace(v); t != "" {
			return t
		}
	}
	return ""
}

// FirstNonEmptyRaw is FirstNonEmpty but returns the value untrimmed. Callers
// that show subprocess output keep its original spacing.
func FirstNonEmptyRaw(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// TruncateRunes clamps s to n runes, marking the cut. n <= 0 returns s.
func TruncateRunes(s string, n int) string {
	if n <= 0 || utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n]) + "…"
}

// TruncateBytes clamps s to n bytes, backing up to a rune boundary so a
// multi-byte character is never split, and marks the cut. n <= 0 returns s.
func TruncateBytes(s string, n int) string {
	if n <= 0 || len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + "…"
}
