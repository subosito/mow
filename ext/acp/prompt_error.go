package acp

import "strings"

// promptErrorMessage is the session/prompt -32603 text. Hosts paint this
// as-is: never dump net/http `Post "http://…": dial tcp …`.
func promptErrorMessage(err error) string {
	if err == nil {
		return "internal error"
	}
	s := strings.TrimSpace(err.Error())
	if s == "" {
		return "internal error"
	}
	sl := strings.ToLower(s)
	switch {
	case strings.Contains(sl, "connection refused"):
		return "provider unavailable (connection refused)"
	case strings.Contains(sl, "connection reset"):
		return "provider unavailable (connection reset)"
	case strings.Contains(sl, "no such host"):
		return "provider unavailable (host not found)"
	case strings.Contains(sl, "network is unreachable"):
		return "provider unavailable (network unreachable)"
	}
	if strings.Contains(s, "://") || strings.Contains(sl, "dial tcp") {
		return "provider unavailable"
	}
	return s
}
