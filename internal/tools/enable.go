package tools

import "strings"

// RegistryNames are the tools Registry can construct. Media generate_*/understand_*
// names are enable-filter aliases (see engine isBuiltin) but are not created here.
func RegistryNames() []string {
	return []string{"read", "glob", "grep", "write", "edit", "bash"}
}

// MediaEnableNames are packs/media tools. They exist only when that pack is
// linked and has registered the tool (model id + API key).
func MediaEnableNames() []string {
	return []string{
		"generate_image", "generate_speech", "generate_video",
		"understand_image", "understand_voice", "understand_video",
	}
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

// HasMediaEnableName reports whether any name is a packs/media tool.
func HasMediaEnableName(names []string) bool {
	media := map[string]bool{}
	for _, n := range MediaEnableNames() {
		media[n] = true
	}
	for _, n := range names {
		if media[strings.ToLower(strings.TrimSpace(n))] {
			return true
		}
	}
	return false
}

// FormatUnregisteredEnable is the doctor / warn line for enable names that
// never made it onto the process tool list. mediaLinked is true when
// packs/media is imported (optional feature "media").
func FormatUnregisteredEnable(names []string, mediaLinked bool) string {
	if len(names) == 0 {
		return ""
	}
	hint := ""
	if HasMediaEnableName(names) && !mediaLinked {
		hint = " (lean mow has no packs/media — use mowx, or drop it from enable)"
	}
	joined := strings.Join(names, ", ")
	if len(names) == 1 {
		return "tools.enable lists " + joined + ", but it is not registered in this binary" + hint
	}
	return "tools.enable lists " + joined + ", but they are not registered in this binary" + hint
}
