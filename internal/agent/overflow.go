package agent

import "strings"

// maxOverflowRetries bounds forced compactions per run after a context
// overflow; past it the provider error is returned as-is.
const maxOverflowRetries = 2

// contextOverflowMarkers are lowercase substrings providers and gateways use
// when a request exceeds the model's context window.
var contextOverflowMarkers = []string{
	"context_length_exceeded",
	"maximum context length",
	"context length",
	"context window",
	"prompt is too long",
	"input is too long",
	"too many tokens",
	"exceeds the context",
	"request too large",
}

// IsContextOverflow reports whether err is a provider rejection of a request
// that is larger than the model's context window. It matches on the error
// text because every wire and gateway phrases it differently and the status
// (400/413) alone is ambiguous.
func IsContextOverflow(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	for _, m := range contextOverflowMarkers {
		if strings.Contains(msg, m) {
			return true
		}
	}
	return false
}
