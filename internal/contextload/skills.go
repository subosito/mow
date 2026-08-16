package contextload

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// LoadSkills collects skill markdown from each dir and concatenates it for the
// system prompt. Skills use the Agent Skills folder layout, one level deep:
//
//	<dir>/<name>/SKILL.md
//
// The folder is the skill and SKILL.md (case-insensitive) is its entry point;
// other files in the folder (README, references, scripts) are not instructions.
// A folder without SKILL.md is skipped. Optional YAML frontmatter is parsed
// per https://agentskills.io/specification — the body (not the fence) is
// injected. Each skill is labeled by spec `name` when valid, else folder name.
//
// Dedup is by lowercased folder NAME with first-directory precedence: when the
// same name appears in multiple dirs (e.g. global and user), only the first
// dir's copy loads. This matches how a user expects "my override wins over
// global" when global comes first in the search path — and keeps the selector,
// explicit loader, and available-names listing on one consistent rule.
func LoadSkills(dirs []string) string {
	return joinSkillSections(collectSkills(dirs, skillLoadAll))
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

// LoadExplicitSkills loads skills whose folder name or spec `name` is in names
// (case-insensitive), regardless of the first-prompt selector. Unknown names
// are silently skipped so config/CLI referencing a skill that exists on one
// machine but not another does not error. Dedupes by lowercased folder NAME
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
	return joinSkillSections(collectSkills(dirs, func(info SkillInfo) bool {
		return want[strings.ToLower(info.Folder)] || want[strings.ToLower(info.Name)]
	}))
}

// AvailableSkillNames returns sorted, deduplicated folder names that contain a
// SKILL.md entry point across all dirs. Used by hosts (e.g. /skill listing)
// to show what is discoverable without injecting skill bodies into the prompt.
// Dedup is by lowercased name with first-directory precedence, so a name
// present in both global and user dirs lists once (the first dir's spelling).
func AvailableSkillNames(dirs []string) []string {
	infos := AvailableSkillInfos(dirs)
	out := make([]string, 0, len(infos))
	for _, info := range infos {
		out = append(out, info.Folder)
	}
	return out
}

// AvailableSkillInfos is AvailableSkillNames plus Agent Skills frontmatter
// (spec name, description). Body is omitted — listing must not inject.
func AvailableSkillInfos(dirs []string) []SkillInfo {
	infos := collectSkills(dirs, skillLoadAll)
	sort.Slice(infos, func(i, j int) bool {
		return strings.ToLower(infos[i].Folder) < strings.ToLower(infos[j].Folder)
	})
	for i := range infos {
		infos[i].Body = ""
	}
	return infos
}

// LoadSelectedSkills loads only skills whose folder name or spec `name`
// occurs in the first user prompt (case-insensitive). Skills with
// `disable-model-invocation: true` are never selected this way (explicit
// --skill / ActivateSkills still can). Disabled selection loads all
// configured skills. An unrelated prompt selects none; mandatory AGENTS
// instructions are loaded separately and are never affected.
func LoadSelectedSkills(dirs []string, prompt string, enabled bool) string {
	if !enabled {
		return LoadSkills(dirs)
	}
	prompt = strings.ToLower(prompt)
	return joinSkillSections(collectSkills(dirs, func(info SkillInfo) bool {
		if info.DisableModelInvocation {
			return false
		}
		return strings.Contains(prompt, strings.ToLower(info.Folder)) ||
			(info.Name != "" && strings.Contains(prompt, strings.ToLower(info.Name)))
	}))
}

type skillFilter func(SkillInfo) bool

func skillLoadAll(SkillInfo) bool { return true }

func collectSkills(dirs []string, keep skillFilter) []SkillInfo {
	var out []SkillInfo
	seen := map[string]bool{} // lowercased folder
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
			path, ok := findSkillFile(filepath.Join(dir, name))
			if !ok {
				continue
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				continue
			}
			info := parseSkillMarkdown(name, string(raw))
			if strings.TrimSpace(info.Body) == "" {
				continue
			}
			if keep != nil && !keep(info) {
				continue
			}
			seen[lc] = true
			out = append(out, info)
		}
	}
	return out
}

func joinSkillSections(infos []SkillInfo) string {
	var parts []string
	for _, info := range infos {
		if sec := formatSkillSection(info); sec != "" {
			parts = append(parts, sec)
		}
	}
	return strings.Join(parts, "\n\n")
}
