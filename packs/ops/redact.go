package ops

import "regexp"

var secretPatterns = []struct {
	re   *regexp.Regexp
	repl string
}{
	// Bearer first: the key=value rule would otherwise eat only "Bearer"
	// and leave the token on the line.
	{regexp.MustCompile(`(?i)bearer\s+[a-z0-9\-._~+/]+=*`), `Bearer [redacted]`},
	// Word-bounded keys: "token" must not match inside csrf_token= or mytoken=.
	{regexp.MustCompile(`(?i)(^|[^A-Za-z0-9_])((?:api[_-]?key|token|secret|password|authorization)\s*[=:]\s*)\S+`), `${1}${2}[redacted]`},
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
