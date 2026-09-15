package tools

import "strings"

// RegistryNames are the tools Registry can construct.
func RegistryNames() []string {
	return []string{"read", "glob", "grep", "write", "edit", "bash"}
}

// UnknownEnable returns enable-list names that are not in registered.
// Comparison is case-insensitive; empty names are skipped; order follows enable.
func UnknownEnable(enable, registered []string) []string {
	have := make(map[string]bool, len(registered))
	for _, n := range registered {
		n = strings.ToLower(strings.TrimSpace(n))
		if n != "" {
			have[n] = true
		}
	}
	var miss []string
	seen := map[string]bool{}
	for _, n := range enable {
		n = strings.ToLower(strings.TrimSpace(n))
		if n == "" || have[n] || seen[n] {
			continue
		}
		seen[n] = true
		miss = append(miss, n)
	}
	return miss
}

// FormatUnregisteredEnable describes configured tools absent from the registry.
func FormatUnregisteredEnable(names []string) string {
	if len(names) == 0 {
		return ""
	}
	joined := strings.Join(names, ", ")
	if len(names) == 1 {
		return "tools.enable lists " + joined + ", but it is not registered in this binary"
	}
	return "tools.enable lists " + joined + ", but they are not registered in this binary"
}
