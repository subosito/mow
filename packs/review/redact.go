package review

import (
	"regexp"
	"strings"
)

// Secret patterns applied to every free-form field before a report is printed
// or written to disk. Review output quotes source code, so a finding about a
// leaked credential must not leak the credential again into CI logs.
var secretPatterns = []*regexp.Regexp{
	// Provider-shaped tokens (prefix + long random tail).
	regexp.MustCompile(`(?i)\b(sk|pk|rk)-[A-Za-z0-9_\-]{16,}`),
	regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9]{20,}`),
	regexp.MustCompile(`\bxox[baprs]-[A-Za-z0-9\-]{10,}`),
	regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`),
	regexp.MustCompile(`\bAIza[0-9A-Za-z_\-]{20,}`),
	// PEM private keys.
	regexp.MustCompile(`(?s)-----BEGIN [A-Z ]*PRIVATE KEY-----.*?-----END [A-Z ]*PRIVATE KEY-----`),
	// JWTs.
	regexp.MustCompile(`\beyJ[A-Za-z0-9_\-]{8,}\.[A-Za-z0-9_\-]{8,}\.[A-Za-z0-9_\-]{8,}`),
	// key = "value" assignments for secret-ish names.
	regexp.MustCompile(`(?i)\b(pass(word)?|passwd|secret|token|api[_\-]?key|access[_\-]?key|private[_\-]?key|credential)s?\s*[:=]\s*["']?[^\s"',;]{8,}`),
	// Basic auth embedded in URLs.
	regexp.MustCompile(`(?i)\b[a-z][a-z0-9+.\-]*://[^\s/@:]+:[^\s/@]{3,}@`),
}

// redactionMark replaces a matched secret.
const redactionMark = "[redacted]"

// redactSecrets masks likely credentials while keeping the surrounding prose
// (and the assignment's key name) readable.
func redactSecrets(s string) string {
	if s == "" {
		return s
	}
	for _, re := range secretPatterns {
		s = re.ReplaceAllStringFunc(s, func(m string) string {
			// Keep "key = " so the reader still knows what was found.
			if i := strings.IndexAny(m, ":="); i >= 0 && !strings.Contains(m[:i], "://") {
				head := m[:i+1]
				if strings.Contains(m, "://") { // URL creds: keep scheme+user
					return redactURLCreds(m)
				}
				return head + " " + redactionMark
			}
			if strings.Contains(m, "://") {
				return redactURLCreds(m)
			}
			return redactionMark
		})
	}
	return s
}

// redactURLCreds masks only the password portion of scheme://user:pass@host.
func redactURLCreds(m string) string {
	at := strings.LastIndex(m, "@")
	if at < 0 {
		return redactionMark
	}
	creds := m[:at]
	i := strings.Index(creds, "://")
	if i < 0 {
		return redactionMark
	}
	userinfo := creds[i+3:]
	colon := strings.Index(userinfo, ":")
	if colon < 0 {
		return m
	}
	return creds[:i+3] + userinfo[:colon] + ":" + redactionMark + "@"
}

// redactFinding applies redaction to every free-form field of a finding.
func redactFinding(f Finding) Finding {
	f.Title = redactSecrets(f.Title)
	f.Evidence = redactSecrets(f.Evidence)
	f.Impact = redactSecrets(f.Impact)
	f.Recommendation = redactSecrets(f.Recommendation)
	f.VerificationNotes = redactSecrets(f.VerificationNotes)
	for i := range f.Locations {
		f.Locations[i].Snippet = redactSecrets(f.Locations[i].Snippet)
	}
	return f
}

// RedactReport masks likely secrets across a whole report (summary and notes
// included). Findings are already redacted by Validate; this covers prose that
// the workflow added afterwards.
func RedactReport(r *Report) *Report {
	if r == nil {
		return nil
	}
	r.Summary = redactSecrets(r.Summary)
	for i := range r.Notes {
		r.Notes[i] = redactSecrets(r.Notes[i])
	}
	for i := range r.Findings {
		r.Findings[i] = redactFinding(r.Findings[i])
	}
	return r
}
