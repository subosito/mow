package agent

import (
	"os"
	"path/filepath"
	"strings"
)

// spillToolResult writes a tool result that is about to be cut to
// dir/tool-results and returns the file path, or "" when dir is unset or the
// write fails (the caller then truncates without a pointer).
func spillToolResult(dir, toolName, body string) string {
	if strings.TrimSpace(dir) == "" {
		return ""
	}
	sub := filepath.Join(dir, "tool-results")
	if err := os.MkdirAll(sub, 0o700); err != nil {
		return ""
	}
	f, err := os.CreateTemp(sub, spillName(toolName)+"-*.txt")
	if err != nil {
		return ""
	}
	_, werr := f.WriteString(body)
	cerr := f.Close()
	if werr != nil || cerr != nil {
		os.Remove(f.Name())
		return ""
	}
	return f.Name()
}

// spillName keeps [A-Za-z0-9_-] so a tool name cannot escape the directory.
func spillName(s string) string {
	out := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
			return r
		}
		return '_'
	}, s)
	if out == "" {
		return "tool"
	}
	return out
}
