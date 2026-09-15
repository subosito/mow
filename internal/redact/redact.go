// Package redact holds the subprocess-stderr ring used by ACP peers and MCP
// stdio servers: retain the newest bytes, never print secrets.
package redact

import (
	"regexp"
	"strings"
	"sync"
)

const defaultCap = 16 << 10

var secretPatterns = []struct {
	re   *regexp.Regexp
	repl string
}{
	// Bearer first: the key=value rule would otherwise eat only "Bearer"
	// from "Authorization: Bearer <token>" and leave the token on the line.
	{regexp.MustCompile(`(?i)bearer\s+[a-z0-9\-._~+/]+=*`), `Bearer [redacted]`},
	{regexp.MustCompile(`(?i)((?:api[_-]?key|token|secret|password|authorization)\s*[=:]\s*)\S+`), `${1}[redacted]`},
	{regexp.MustCompile(`sk-[A-Za-z0-9_-]{10,}`), `sk-[redacted]`},
	{regexp.MustCompile(`(?i)((?:x-api-key|x-auth-token)\s*:\s*)\S+`), `${1}[redacted]`},
}

// Ring retains the newest subprocess stderr without forwarding secrets.
type Ring struct {
	mu  sync.Mutex
	buf []byte
	cap int
}

// NewRing returns a ring that keeps at most cap bytes. cap <= 0 uses 16KiB.
func NewRing(cap int) *Ring {
	if cap <= 0 {
		cap = defaultCap
	}
	return &Ring{cap: cap}
}

func (r *Ring) Write(p []byte) (int, error) {
	if r == nil || len(p) == 0 {
		return len(p), nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.buf = append(r.buf, p...)
	if len(r.buf) > r.cap {
		start := len(r.buf) - r.cap
		for start < len(r.buf) && r.buf[start]&0xc0 == 0x80 {
			start++
		}
		r.buf = append([]byte(nil), r.buf[start:]...)
	}
	return len(p), nil
}

// Tail returns a sanitized copy of the retained stderr (may be empty).
func (r *Ring) Tail() string {
	if r == nil {
		return ""
	}
	r.mu.Lock()
	raw := string(r.buf)
	r.mu.Unlock()
	return Sanitize(raw)
}

// Len is the unsanitized retained byte count.
func (r *Ring) Len() int {
	if r == nil {
		return 0
	}
	r.mu.Lock()
	n := len(r.buf)
	r.mu.Unlock()
	return n
}

// Sanitize redacts secret-looking spans and keeps the last 24 lines / 2000 runes.
func Sanitize(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	for _, p := range secretPatterns {
		s = p.re.ReplaceAllString(s, p.repl)
	}
	lines := strings.Split(s, "\n")
	const maxLines = 24
	if len(lines) > maxLines {
		s = strings.Join(lines[len(lines)-maxLines:], "\n")
	}
	const maxRunes = 2000
	rs := []rune(s)
	if len(rs) > maxRunes {
		s = string(rs[len(rs)-maxRunes:])
	}
	return strings.TrimSpace(s)
}
