// Package gitx runs git for extensions that manage worktrees (ext/goal,
// ext/worktree): bounded, cancellable, and with credentials redacted from any
// output that reaches an error message.
package gitx

import (
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

// Timeout bounds one git invocation so a hung remote or credential prompt
// cannot stall the caller forever (a parent ctx cancel still applies first).
const Timeout = 10 * time.Minute

// maxErrOutput caps the git output folded into an error message.
const maxErrOutput = 400

// Run runs one git command in dir and returns trimmed combined output. On
// failure the error carries a redacted, truncated copy of that output.
func Run(ctx context.Context, dir string, args ...string) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	tctx, cancel := context.WithTimeout(ctx, Timeout)
	defer cancel()
	cmd := exec.CommandContext(tctx, "git", args...)
	cmd.Dir = dir
	setProcAttr(cmd)
	out, err := cmd.CombinedOutput()
	text := strings.TrimSpace(string(out))
	if err != nil {
		msg := Redact(truncate(text, maxErrOutput))
		return msg, fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, msg)
	}
	return text, nil
}

var secretPatterns = []struct {
	re   *regexp.Regexp
	repl string
}{
	{regexp.MustCompile(`(?i)((?:api[_-]?key|token|secret|password|authorization)\s*[=:]\s*)\S+`), `${1}[redacted]`},
	{regexp.MustCompile(`(?i)bearer\s+[a-z0-9\-._~+/]+=*`), `Bearer [redacted]`},
	{regexp.MustCompile(`sk-[a-zA-Z0-9]{10,}`), `sk-[redacted]`},
	{regexp.MustCompile(`(?i)((?:x-api-key|x-auth-token)\s*:\s*)\S+`), `${1}[redacted]`},
	// Remote URLs with inline credentials (https://user:token@host/…).
	{regexp.MustCompile(`(?i)(https?://[^\s/]+:)[^\s@/]+@`), `${1}[redacted]@`},
}

// Redact masks credentials git may echo (remote URLs, auth headers, tokens).
func Redact(s string) string {
	for _, p := range secretPatterns {
		s = p.re.ReplaceAllString(s, p.repl)
	}
	return s
}

func truncate(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n]) + "…"
}
