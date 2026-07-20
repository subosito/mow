package contextload

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// LoadSkills reads *.md from dirs (non-recursive) and concatenates them for the system prompt.
func LoadSkills(dirs []string) string {
	var parts []string
	seen := map[string]bool{}
	for _, dir := range dirs {
		dir = strings.TrimSpace(dir)
		if dir == "" {
			continue
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		var names []string
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			n := e.Name()
			if !strings.HasSuffix(strings.ToLower(n), ".md") {
				continue
			}
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			p := filepath.Join(dir, n)
			if seen[p] {
				continue
			}
			seen[p] = true
			b, err := os.ReadFile(p)
			if err != nil {
				continue
			}
			if s := strings.TrimSpace(string(b)); s != "" {
				parts = append(parts, "## skill: "+n+"\n\n"+s)
			}
		}
	}
	return strings.Join(parts, "\n\n")
}
