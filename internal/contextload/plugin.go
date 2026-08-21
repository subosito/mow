package contextload

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// PluginInfo is one Agent Plugin (https://agent-plugins.org/specification):
// a folder with plugin.json that may ship a skills/ directory of Agent Skills.
// MCP servers declared on the plugin are not registered here — packs/mcp stays
// the tool surface. /plugins lists installs; /skills still activates.
type PluginInfo struct {
	// ID is the on-disk directory name (stable key).
	ID string
	// Name is plugin.json `name` when set, otherwise ID.
	Name string
	Version     string
	Description string
	// Path is the plugin root (contains plugin.json).
	Path string
	// SkillsDir is the resolved skills/ directory when it exists.
	SkillsDir string
	// SkillFolders are Agent Skills folder names under SkillsDir.
	SkillFolders []string
	// DefaultSkills are plugin.json default-skills / defaultSkills names
	// to load like --skill (folder or spec name).
	DefaultSkills []string
	// Always means every skill in this plugin is treated as default.
	Always bool
}

type pluginJSON struct {
	Name           string          `json:"name"`
	Version        string          `json:"version"`
	Description    string          `json:"description"`
	Skills         string          `json:"skills"`
	Always         bool            `json:"always"`
	DefaultSkills  jsonStringSlice `json:"default-skills"`
	DefaultSkills2 jsonStringSlice `json:"defaultSkills"`
}

// jsonStringSlice accepts a JSON string or array of strings.
type jsonStringSlice []string

func (s *jsonStringSlice) UnmarshalJSON(b []byte) error {
	if string(b) == "null" {
		return nil
	}
	var one string
	if err := json.Unmarshal(b, &one); err == nil {
		one = strings.TrimSpace(one)
		if one != "" {
			*s = []string{one}
		}
		return nil
	}
	var many []string
	if err := json.Unmarshal(b, &many); err != nil {
		return nil // ignore unknown shapes
	}
	var out []string
	for _, n := range many {
		n = strings.TrimSpace(n)
		if n != "" {
			out = append(out, n)
		}
	}
	*s = out
	return nil
}

// ListPlugins walks plugin roots (each child dir with plugin.json).
// Missing roots are skipped. Dedup is by lowercased directory name,
// first-root precedence (same rule as skills).
func ListPlugins(roots []string) []PluginInfo {
	var out []PluginInfo
	seen := map[string]bool{}
	for _, root := range roots {
		root = strings.TrimSpace(root)
		if root == "" {
			continue
		}
		entries, err := os.ReadDir(root)
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
			info, ok := readPlugin(filepath.Join(root, name), name)
			if !ok {
				continue
			}
			seen[lc] = true
			out = append(out, info)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return strings.ToLower(out[i].ID) < strings.ToLower(out[j].ID)
	})
	return out
}

// PluginSkillDirs returns existing skills/ directories from discovered plugins,
// in ListPlugins order (already first-root precedence).
func PluginSkillDirs(roots []string) []string {
	var out []string
	for _, p := range ListPlugins(roots) {
		if p.SkillsDir != "" {
			out = append(out, p.SkillsDir)
		}
	}
	return out
}

// PluginDefaultSkillNames returns default/always skill names from plugins
// (for merging into explicit skills). Deduped, first-seen order.
func PluginDefaultSkillNames(roots []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, p := range ListPlugins(roots) {
		names := p.DefaultSkills
		if p.Always {
			names = append(append([]string{}, p.SkillFolders...), names...)
		}
		for _, n := range names {
			k := strings.ToLower(strings.TrimSpace(n))
			if k == "" || seen[k] {
				continue
			}
			seen[k] = true
			out = append(out, n)
		}
	}
	return out
}

func readPlugin(dir, id string) (PluginInfo, bool) {
	raw, err := os.ReadFile(filepath.Join(dir, "plugin.json"))
	if err != nil {
		return PluginInfo{}, false
	}
	var meta pluginJSON
	if err := json.Unmarshal(raw, &meta); err != nil {
		return PluginInfo{}, false
	}
	info := PluginInfo{
		ID:          id,
		Name:        strings.TrimSpace(meta.Name),
		Version:     strings.TrimSpace(meta.Version),
		Description: strings.TrimSpace(meta.Description),
		Path:        dir,
		Always:      meta.Always,
	}
	if info.Name == "" {
		info.Name = id
	}
	rel := strings.TrimSpace(meta.Skills)
	if rel == "" {
		rel = "skills"
	}
	rel = filepath.Clean(rel)
	if rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) || filepath.IsAbs(rel) {
		rel = "skills"
	}
	skillsDir := filepath.Join(dir, rel)
	if fi, err := os.Stat(skillsDir); err == nil && fi.IsDir() {
		info.SkillsDir = skillsDir
		info.SkillFolders = AvailableSkillNames([]string{skillsDir})
	}
	defaults := append([]string{}, meta.DefaultSkills...)
	defaults = append(defaults, meta.DefaultSkills2...)
	info.DefaultSkills = defaults
	return info, true
}
