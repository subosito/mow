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

// AvailableSkillNames returns the sorted, deduplicated skill folder names
// that contain a SKILL.md entry point across the given directories. It lists
// what is discoverable without reading skill bodies. Hosts (e.g. the TUI
// /skill command) use it so users know what names to pass to --skill.
func AvailableSkillNames(dirs []string) []string {
	return contextload.AvailableSkillNames(dirs)
}
