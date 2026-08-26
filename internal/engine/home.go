package engine

import (
	"github.com/subosito/mow/internal/config"
	"github.com/subosito/mow/internal/contextload"
)

// Home returns the mow user data directory.
// Override with MOW_HOME (default ~/.mow). See config.Home for details.
func Home() string {
	return config.Home()
}

// SkillsDir returns the global skills directory ($MOW_HOME/skills).
// This is the same path Engine.New prepends to the skill dir list.
func SkillsDir() string {
	return config.SkillsDir()
}

// PluginsDir returns the global plugin directory ($MOW_HOME/plugins).
func PluginsDir() string {
	return config.PluginsDir()
}

// PluginInfo is one Agent Plugin (plugin.json + optional skills/hooks/MCP).
type PluginInfo = contextload.PluginInfo

// PluginMCPServer is one mcpServers entry from a plugin manifest.
type PluginMCPServer = contextload.PluginMCPServer

// ListPlugins walks plugin roots (each child dir with plugin.json).
func ListPlugins(roots []string) []PluginInfo {
	return contextload.ListPlugins(roots)
}

// HostOwnedPluginRoots is $MOW_HOME/plugins plus workspace-profile plugins/
// directories derived from overlay config paths (the overlay path is
// recognized even when config.yaml is absent). Project .mow/plugins is not
// included.
func HostOwnedPluginRoots(home string, configPaths []string) []string {
	return contextload.HostOwnedPluginRoots(home, configPaths)
}

// AvailableSkillNames returns the sorted, deduplicated skill folder names
// that contain a SKILL.md entry point across the given directories. It lists
// what is discoverable without reading skill bodies. Hosts (e.g. the TUI
// /skill command) use it so users know what names to pass to --skill.
func AvailableSkillNames(dirs []string) []string {
	return contextload.AvailableSkillNames(dirs)
}

// SkillInfo is one Agent Skills entry (folder + optional SKILL.md frontmatter).
type SkillInfo = contextload.SkillInfo

// AvailableSkillInfos is AvailableSkillNames plus Agent Skills frontmatter.
func AvailableSkillInfos(dirs []string) []SkillInfo {
	return contextload.AvailableSkillInfos(dirs)
}
