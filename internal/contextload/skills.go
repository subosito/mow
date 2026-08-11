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
//
// Dedup is by lowercased skill NAME with first-directory precedence: when the
// same name appears in multiple dirs (e.g. global and user), only the first
// dir's copy loads. This matches how a user expects "my override wins over
// global" when global comes first in the search path — and keeps the selector,
// explicit loader, and available-names listing on one consistent rule.
func LoadSkills(dirs []string) string {
	var parts []string
	seen := map[string]bool{} // lowercased name
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
			lc := strings.ToLower(name)
			if seen[lc] {
				continue
			}
			p, ok := findSkillFile(filepath.Join(dir, name))
			if !ok {
				continue
			}
			b, err := os.ReadFile(p)
			if err != nil {
				continue
			}
			if s := strings.TrimSpace(string(b)); s != "" {
				parts = append(parts, "## skill: "+name+"\n\n"+s)
				seen[lc] = true
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

// LoadExplicitSkills loads skills whose folder name is in names
// (case-insensitive), regardless of the first-prompt selector. Unknown names
// are silently skipped so config/CLI referencing a skill that exists on one
// machine but not another does not error. Dedupes by lowercased skill NAME
// with first-directory precedence (see LoadSkills). Sorted by name within
// each dir for stable prompt ordering.
func LoadExplicitSkills(dirs []string, names []string) string {
	want := make(map[string]bool)
	for _, n := range names {
		n = strings.TrimSpace(strings.ToLower(n))
		if n != "" {
			want[n] = true
		}
	}
	if len(want) == 0 {
		return ""
	}
	var parts []string
	seen := map[string]bool{} // lowercased name
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
			lc := strings.ToLower(name)
			if seen[lc] || !want[lc] {
				continue
			}
			p, ok := findSkillFile(filepath.Join(dir, name))
			if !ok {
				continue
			}
			b, err := os.ReadFile(p)
			if err != nil {
				continue
			}
			if s := strings.TrimSpace(string(b)); s != "" {
				parts = append(parts, "## skill: "+name+"\n\n"+s)
				seen[lc] = true
			}
		}
	}
	return strings.Join(parts, "\n\n")
}

// AvailableSkillNames returns sorted, deduplicated folder names that contain a
// SKILL.md entry point across all dirs. Used by hosts (e.g. /skill listing)
// to show what is discoverable without injecting skill bodies into the prompt.
// Dedup is by lowercased name with first-directory precedence, so a name
// present in both global and user dirs lists once (the first dir's spelling).
func AvailableSkillNames(dirs []string) []string {
	seen := map[string]bool{}
	var out []string
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
			lc := strings.ToLower(name)
			if seen[lc] {
				continue
			}
			if _, ok := findSkillFile(filepath.Join(dir, name)); !ok {
				continue
			}
			seen[lc] = true
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// LoadSelectedSkills loads only skills whose folder name occurs in the first
// user prompt (case-insensitive). Disabled selection loads all configured
// skills. An unrelated prompt selects none; mandatory AGENTS instructions are
// loaded separately and are never affected. Dedup is by lowercased skill name
// with first-directory precedence (see LoadSkills).
func LoadSelectedSkills(dirs []string, prompt string, enabled bool) string {
	if !enabled {
		return LoadSkills(dirs)
	}
	prompt = strings.ToLower(prompt)
	var parts []string
	seen := map[string]bool{} // lowercased name
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
			lc := strings.ToLower(name)
			if seen[lc] || !strings.Contains(prompt, lc) {
				continue
			}
			path, ok := findSkillFile(filepath.Join(dir, name))
			if !ok {
				continue
			}
			body, err := os.ReadFile(path)
			if err != nil {
				continue
			}
			if text := strings.TrimSpace(string(body)); text != "" {
				parts = append(parts, "## skill: "+name+"\n\n"+text)
				seen[lc] = true
			}
		}
	}
	sort.Strings(parts)
	return strings.Join(parts, "\n\n")
}
