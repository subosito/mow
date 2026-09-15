package goal

import "regexp"

var secretPatterns = []struct {
	re   *regexp.Regexp
	repl string
}{
	{regexp.MustCompile(`(?i)((?:api[_-]?key|token|secret|password|authorization)\s*[=:]\s*)\S+`), `${1}[redacted]`},
	{regexp.MustCompile(`(?i)bearer\s+[a-z0-9\-._~+/]+=*`), `Bearer [redacted]`},
	{regexp.MustCompile(`sk-[a-zA-Z0-9]{10,}`), `sk-[redacted]`},
	{regexp.MustCompile(`(?i)((?:x-api-key|x-auth-token)\s*:\s*)\S+`), `${1}[redacted]`},
	{regexp.MustCompile(`(?i)(https?://[^\s/]+:)[^\s@/]+@`), `${1}[redacted]@`},
}

func redactSecrets(s string) string {
	for _, p := range secretPatterns {
		s = p.re.ReplaceAllString(s, p.repl)
	}
	return s
}
