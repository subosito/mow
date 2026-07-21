package contextload

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// LoadSkills collects skill markdown from each dir and concatenates it for the
// system prompt. Skills use the standard folder layout, one level deep:
//
//	<dir>/<name>/SKILL.md
//
// The folder is the skill and SKILL.md (case-insensitive) is its entry point;
// other files in the folder (README, references) are not instructions. A
// folder without SKILL.md is skipped. Each skill is labeled by folder name so
// the model can cite it.
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
		var folders []string
		for _, e := range entries {
			if e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
				folders = append(folders, e.Name())
			}
		}
		sort.Strings(folders)
		for _, name := range folders {
			p, ok := findSkillFile(filepath.Join(dir, name))
			if !ok || seen[p] {
				continue
			}
			seen[p] = true
			b, err := os.ReadFile(p)
			if err != nil {
				continue
			}
			if s := strings.TrimSpace(string(b)); s != "" {
				parts = append(parts, "## skill: "+name+"\n\n"+s)
			}
		}
	}
	return strings.Join(parts, "\n\n")
}

// findSkillFile returns the SKILL.md inside a skill folder (case-insensitive),
// if present.
func findSkillFile(folder string) (string, bool) {
	entries, err := os.ReadDir(folder)
	if err != nil {
		return "", false
	}
	for _, e := range entries {
		if !e.IsDir() && strings.EqualFold(e.Name(), "SKILL.md") {
			return filepath.Join(folder, e.Name()), true
		}
	}
	return "", false
}
