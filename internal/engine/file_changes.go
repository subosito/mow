package engine

import (
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"

	"github.com/subosito/mow/internal/llm"
)

// runFileChanges derives a bounded path-only summary from successful structured
// mutation calls. A tool result beginning with error: never counts as a change.
// Shell effects and arbitrary extension tools are deliberately not guessed.
func runFileChanges(prior, messages []llm.Message) []FileChange {
	if len(prior) > len(messages) {
		return nil
	}
	messages = messages[len(prior):]
	calls := map[string]struct {
		name string
		path string
	}{}
	changes := map[string]string{}
	for _, m := range messages {
		if m.Role == "assistant" {
			for _, tc := range m.ToolCalls {
				if tc.ID == "" || (tc.Function.Name != "write" && tc.Function.Name != "edit" && tc.Function.Name != "apply_patch") {
					continue
				}
				if tc.Function.Name == "apply_patch" {
					var args struct {
						Patch string `json:"patch"`
					}
					if json.Unmarshal([]byte(tc.Function.Arguments), &args) == nil {
						for _, path := range patchPaths(args.Patch) {
							calls[tc.ID+"\x00"+path] = struct{ name, path string }{tc.Function.Name, path}
						}
					}
					continue
				}
				var args struct {
					Path string `json:"path"`
				}
				if json.Unmarshal([]byte(tc.Function.Arguments), &args) == nil && cleanChangePath(args.Path) != "" {
					calls[tc.ID] = struct{ name, path string }{tc.Function.Name, cleanChangePath(args.Path)}
				}
			}
			continue
		}
		if m.Role != "tool" || strings.HasPrefix(strings.TrimSpace(m.Content), "error:") {
			continue
		}
		for key, call := range calls {
			id := key
			if before, _, ok := strings.Cut(key, "\x00"); ok {
				id = before
			}
			if id != m.ToolCallID {
				continue
			}
			op := "modify"
			if call.name == "write" && strings.Contains(m.Content, "created ") {
				op = "add"
			}
			changes[call.path] = op
		}
	}
	paths := make([]string, 0, len(changes))
	for path := range changes {
		paths = append(paths, path)
	}
	slices.Sort(paths)
	out := make([]FileChange, 0, min(len(paths), 256))
	for _, path := range paths[:min(len(paths), 256)] {
		out = append(out, FileChange{Path: path, Op: changes[path]})
	}
	return out
}

func cleanChangePath(path string) string {
	path = filepath.ToSlash(filepath.Clean(strings.TrimSpace(path)))
	if path == "" || path == "." || filepath.IsAbs(path) || path == ".." || strings.HasPrefix(path, "../") {
		return ""
	}
	return path
}

func patchPaths(patch string) []string {
	var out []string
	for line := range strings.SplitSeq(patch, "\n") {
		if !strings.HasPrefix(line, "+++ ") {
			continue
		}
		path := strings.TrimSpace(strings.TrimPrefix(line, "+++ "))
		if path == "/dev/null" {
			continue
		}
		path = strings.TrimPrefix(path, "b/")
		if p := cleanChangePath(path); p != "" && !slices.Contains(out, p) {
			out = append(out, p)
		}
	}
	return out
}
