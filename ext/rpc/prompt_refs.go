package rpc

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

const maxPromptRefBytes = 100_000

var promptFileRefRE = regexp.MustCompile(`@([A-Za-z0-9._/\-]+)`)

// expandPromptFileRefs resolves prompt @path references through Engine.ResolvePath,
// so workspace/extra-root jail policy remains authoritative. Missing, denied,
// and directory references are left unexpanded rather than turning ordinary @
// text into a transport error.
func (s *Server) expandPromptFileRefs(text string) (string, []string) {
	if s.Engine == nil || !strings.Contains(text, "@") {
		return text, nil
	}
	var body strings.Builder
	var attached []string
	seen := map[string]bool{}
	for _, match := range promptFileRefRE.FindAllStringSubmatch(text, -1) {
		ref := strings.TrimRight(match[1], ".,;:)")
		if ref == "" || seen[ref] {
			continue
		}
		abs, err := s.Engine.ResolvePath(ref)
		if err != nil {
			continue
		}
		info, err := os.Stat(abs)
		if err != nil || info.IsDir() {
			continue
		}
		data, err := os.ReadFile(abs)
		if err != nil {
			continue
		}
		if len(data) > maxPromptRefBytes {
			data = append(append([]byte{}, data[:maxPromptRefBytes]...), []byte("\n… (truncated)")...)
		}
		seen[ref] = true
		attached = append(attached, ref)
		fmt.Fprintf(&body, "\n\n--- %s ---\n```%s\n", ref, promptRefLanguage(ref))
		body.Write(data)
		body.WriteString("\n```")
	}
	if len(attached) == 0 {
		return text, nil
	}
	return text + "\n" + body.String(), attached
}

func promptRefLanguage(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".go":
		return "go"
	case ".rs":
		return "rust"
	case ".js", ".jsx":
		return "javascript"
	case ".ts", ".tsx":
		return "typescript"
	case ".py":
		return "python"
	case ".sh", ".bash":
		return "bash"
	case ".json":
		return "json"
	case ".yaml", ".yml":
		return "yaml"
	case ".md":
		return "markdown"
	case ".toml":
		return "toml"
	default:
		return ""
	}
}
