package mcp

import (
	"fmt"
	"regexp"
	"strings"
	"sync"
)

var stderrSecretPatterns = []struct {
	re   *regexp.Regexp
	repl string
}{
	{regexp.MustCompile(`(?i)((?:api[_-]?key|token|secret|password|authorization)\s*[=:]\s*)\S+`), `${1}[redacted]`},
	{regexp.MustCompile(`(?i)bearer\s+[a-z0-9\-._~+/]+=*`), `Bearer [redacted]`},
	{regexp.MustCompile(`sk-[a-zA-Z0-9]{10,}`), `sk-[redacted]`},
	{regexp.MustCompile(`(?i)((?:x-api-key|x-auth-token)\s*:\s*)\S+`), `${1}[redacted]`},
}

// stderrRing retains the newest subprocess stderr without forwarding secrets to the terminal.
type stderrRing struct {
	mu  sync.Mutex
	buf []byte
	cap int
}

func newStderrRing(cap int) *stderrRing {
	if cap <= 0 {
		cap = maxStderrRetain
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
		start := len(r.buf) - r.cap
		for start < len(r.buf) && r.buf[start]&0xc0 == 0x80 {
			start++
		}
		r.buf = append([]byte(nil), r.buf[start:]...)
	}
	return len(p), nil
}

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
	lines := strings.Split(s, "\n")
	const maxLines = 24
	if len(lines) > maxLines {
		lines = lines[len(lines)-maxLines:]
		s = strings.Join(lines, "\n")
	}
	const maxRunes = 2000
	rs := []rune(s)
	if len(rs) > maxRunes {
		s = string(rs[len(rs)-maxRunes:])
	}
	return strings.TrimSpace(s)
}

func appendStderrHint(err error, tail string) error {
	if err == nil || strings.TrimSpace(tail) == "" {
		return err
	}
	return fmt.Errorf("%w\nmcp stderr (tail):\n%s", err, tail)
}

func aggregateToolText(texts ...string) (string, error) {
	var b strings.Builder
	for _, text := range texts {
		if text == "" {
			continue
		}
		need := len(text)
		if b.Len() > 0 {
			need++
		}
		if b.Len()+need > maxToolOutputBytes {
			return "", fmt.Errorf("mcp tool output exceeds %d bytes", maxToolOutputBytes)
		}
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(text)
	}
	return strings.TrimSpace(b.String()), nil
}
