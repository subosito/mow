package contextload

import (
	"cmp"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// PluginInfo is one Agent Plugin (https://agent-plugins.org/specification):
// a folder with plugin.json (or .claude-plugin/plugin.json) that may ship
// skills/, hooks/, and mcpServers. Skills load here; MCP and cmdhook consume
// MCPServers / HooksFile from host-owned roots only.
type PluginInfo struct {
	// ID is the on-disk directory name (stable key).
	ID string
	// Name is plugin.json `name` when set, otherwise ID.
	Name        string
	Version     string
	Description string
	// Path is the plugin root (contains plugin.json or .claude-plugin/).
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
	// MCPServers are stdio/HTTP servers declared on the plugin. Empty when
	// the manifest has none. Callers must not start these from a project
	// plugin root — only HostOwnedPluginRoots.
	MCPServers []PluginMCPServer
	// HooksFile is an existing hooks/hooks.json relative to Path, ready for
	// cmdhook. Empty when the plugin has no hooks.
	HooksFile string
}

// PluginMCPServer is one mcpServers entry from plugin.json.
type PluginMCPServer struct {
	Name    string
	Command string
	Args    []string
	Env     map[string]string
	URL     string
}

type pluginJSON struct {
	Name           string                     `json:"name"`
	Version        string                     `json:"version"`
	Description    string                     `json:"description"`
	Skills         string                     `json:"skills"`
	Always         bool                       `json:"always"`
	DefaultSkills  jsonStringSlice            `json:"default-skills"`
	DefaultSkills2 jsonStringSlice            `json:"defaultSkills"`
	MCPServers     map[string]pluginMCPServer `json:"mcpServers"`
}

type pluginMCPServer struct {
	Command any               `json:"command"`
	Args    []string          `json:"args"`
	Env     map[string]string `json:"env"`
	URL     string            `json:"url"`
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
		slices.Sort(folders)
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
	slices.SortFunc(out, func(a, b PluginInfo) int {
		return cmp.Compare(strings.ToLower(a.ID), strings.ToLower(b.ID))
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
	raw, err := readPluginManifest(dir)
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
	info.MCPServers = parsePluginMCPServers(meta.MCPServers, dir)
	info.HooksFile = resolveHooksFile(dir)
	return info, true
}

func readPluginManifest(dir string) ([]byte, error) {
	for _, rel := range []string{
		"plugin.json",
		filepath.Join(".claude-plugin", "plugin.json"),
	} {
		raw, err := os.ReadFile(filepath.Join(dir, rel))
		if err == nil {
			return raw, nil
		}
	}
	return nil, os.ErrNotExist
}

func parsePluginMCPServers(raw map[string]pluginMCPServer, pluginRoot string) []PluginMCPServer {
	if len(raw) == 0 {
		return nil
	}
	names := make([]string, 0, len(raw))
	for name := range raw {
		names = append(names, name)
	}
	slices.Sort(names)
	var out []PluginMCPServer
	for _, name := range names {
		e := raw[name]
		cmd, extra := parsePluginCommand(e.Command)
		args := append(append([]string{}, extra...), e.Args...)
		cmd = ExpandPluginVars(cmd, pluginRoot)
		for i, a := range args {
			args[i] = ExpandPluginVars(a, pluginRoot)
		}
		env := map[string]string{}
		for k, v := range e.Env {
			env[k] = ExpandPluginVars(v, pluginRoot)
		}
		out = append(out, PluginMCPServer{
			Name:    strings.TrimSpace(name),
			Command: strings.TrimSpace(cmd),
			Args:    args,
			Env:     env,
			URL:     strings.TrimSpace(e.URL),
		})
	}
	return out
}

func parsePluginCommand(v any) (command string, args []string) {
	switch t := v.(type) {
	case string:
		return strings.TrimSpace(t), nil
	case []any:
		var parts []string
		for _, x := range t {
			s, ok := x.(string)
			if !ok {
				continue
			}
			s = strings.TrimSpace(s)
			if s != "" {
				parts = append(parts, s)
			}
		}
		if len(parts) == 0 {
			return "", nil
		}
		return parts[0], parts[1:]
	default:
		return "", nil
	}
}

func resolveHooksFile(dir string) string {
	path := filepath.Join(dir, "hooks", "hooks.json")
	if fi, err := os.Stat(path); err == nil && fi.Mode().IsRegular() {
		return filepath.Join("hooks", "hooks.json")
	}
	return ""
}

// HostOwnedPluginRoots is $MOW_HOME/plugins plus workspace-profile plugins/
// directories derived from overlay config paths. A profile overlay is
// recognized from $MOW_HOME/workspaces/<name>/config.yaml even when that
// file does not exist (plugins-only profiles). Project .mow/plugins is not
// included — skills may load from there; MCP and hooks must not auto-spawn
// from the workspace.
func HostOwnedPluginRoots(home string, configPaths []string) []string {
	home = strings.TrimSpace(home)
	var out []string
	if home != "" {
		out = append(out, filepath.Join(home, "plugins"))
	}
	ws := ""
	if home != "" {
		ws = filepath.Join(filepath.Clean(home), "workspaces")
	}
	seen := map[string]bool{}
	for _, p := range out {
		seen[p] = true
	}
	for _, p := range configPaths {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		p = filepath.Clean(p)
		if !strings.EqualFold(filepath.Base(p), "config.yaml") {
			continue
		}
		dir := filepath.Dir(p)
		if ws == "" || filepath.Dir(dir) != ws {
			continue
		}
		plug := filepath.Join(dir, "plugins")
		if seen[plug] {
			continue
		}
		seen[plug] = true
		out = append(out, plug)
	}
	return out
}

// ExpandPluginVars replaces ${CLAUDE_PLUGIN_ROOT} the way Claude Code does.
func ExpandPluginVars(s, pluginRoot string) string {
	if s == "" || pluginRoot == "" {
		return s
	}
	return strings.ReplaceAll(s, "${CLAUDE_PLUGIN_ROOT}", pluginRoot)
}
