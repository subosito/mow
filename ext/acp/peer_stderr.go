package acp

import (
	"regexp"
	"strings"
	"sync"
)

// defaultStderrCap is the max bytes retained from a peer process stderr.
const defaultStderrCap = 16 << 10

// Patterns redact secret-looking spans. Replacements must not rely on $1 unless
// the pattern has a capture group (bearer/sk- patterns are whole-match only).
var stderrSecretPatterns = []struct {
	re   *regexp.Regexp
	repl string
}{
	{regexp.MustCompile(`(?i)((?:api[_-]?key|token|secret|password|authorization)\s*[=:]\s*)\S+`), `${1}[redacted]`},
	{regexp.MustCompile(`(?i)bearer\s+[a-z0-9\-._~+/]+=*`), `Bearer [redacted]`},
	{regexp.MustCompile(`sk-[a-zA-Z0-9]{10,}`), `sk-[redacted]`},
	{regexp.MustCompile(`(?i)((?:x-api-key|x-auth-token)\s*:\s*)\S+`), `${1}[redacted]`},
}

// stderrRing retains the newest stderr bytes from a peer subprocess.
type stderrRing struct {
	mu  sync.Mutex
	buf []byte
	cap int
}

func newStderrRing(cap int) *stderrRing {
	if cap <= 0 {
		cap = defaultStderrCap
	}
	return &stderrRing{cap: cap}
}

func (r *stderrRing) Write(p []byte) (int, error) {
	if r == nil || len(p) == 0 {
		return len(p), nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.buf = append(r.buf, p...)
	if len(r.buf) > r.cap {
		// Drop the oldest bytes; skip partial leading UTF-8 continuation so
		// tail() does not start mid-rune when the cut lands inside a codepoint.
		start := len(r.buf) - r.cap
		for start < len(r.buf) && r.buf[start]&0xc0 == 0x80 {
			start++
		}
		r.buf = append([]byte(nil), r.buf[start:]...)
	}
	return len(p), nil
}

// tail returns a sanitized copy of the retained stderr (may be empty).
func (r *stderrRing) tail() string {
	if r == nil {
		return ""
	}
	r.mu.Lock()
	raw := string(r.buf)
	r.mu.Unlock()
	return sanitizeStderrTail(raw)
}

func sanitizeStderrTail(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	for _, p := range stderrSecretPatterns {
		s = p.re.ReplaceAllString(s, p.repl)
	}
	// Keep only the last few lines for error messages.
	lines := strings.Split(s, "\n")
	const maxLines = 24
	if len(lines) > maxLines {
		lines = lines[len(lines)-maxLines:]
		s = strings.Join(lines, "\n")
	}
	const maxRunes = 2000
	rs := []rune(s)
	if len(rs) > maxRunes {
		// Avoid starting mid-rune after a hard cap (already rune-sliced).
		s = string(rs[len(rs)-maxRunes:])
	}
	return strings.TrimSpace(s)
}

func appendStderrHint(err error, tail string) error {
	if err == nil || strings.TrimSpace(tail) == "" {
		return err
	}
	return &stderrError{err: err, tail: tail}
}

type stderrError struct {
	err  error
	tail string
}

func (e *stderrError) Error() string {
	if e == nil || e.err == nil {
		return ""
	}
	return e.err.Error() + "\npeer stderr (tail):\n" + e.tail
}

func (e *stderrError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.err
}
