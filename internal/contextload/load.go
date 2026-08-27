// Package contextload loads AGENTS.md / CLAUDE.md instruction files.
package contextload

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/subosito/mow/internal/config"
)

// Load walks from workspace up to root collecting instruction files, then
// prepends optional global layers when includeGlobal is true:
// ~/.agents/AGENTS.md (shared base) then $MOW_HOME/AGENTS.md (mow overlay).
func Load(workspace string) (string, error) {
	return LoadWithExtras(workspace)
}

// LoadHermetic is Load without global home files (~/.agents/AGENTS.md and
// $MOW_HOME/AGENTS.md). Used by hermetic Engine construction
// (Options.LoadUserConfig=false) so embedding never pulls operator-home
// instructions. Workspace-chain files still load (project files).
func LoadHermetic(workspace string) (string, error) {
	return loadAgents(workspace, false)
}

// LoadWithExtras is Load with extra AGENTS-style files inserted between the
// global home files and the workspace chain (e.g. a workspace profile's
// AGENTS.md: more specific than global, less than the workspace).
func LoadWithExtras(workspace string, extraPaths ...string) (string, error) {
	return loadAgents(workspace, true, extraPaths...)
}

func loadAgents(workspace string, includeGlobal bool, extraPaths ...string) (string, error) {
	var parts []string
	if includeGlobal {
		if std := config.AgentsStandardDir(); std != "" {
			if s := readTrimmed(filepath.Join(std, "AGENTS.md")); s != "" {
				parts = append(parts, s)
			}
		}
		if s := readTrimmed(config.AgentsPath()); s != "" {
			parts = append(parts, s)
		}
	}
	for _, p := range extraPaths {
		if s := readTrimmed(p); s != "" {
			parts = append(parts, s)
		}
	}
	dir, err := filepath.Abs(workspace)
	if err != nil {
		return "", err
	}
	var layers [][]string
	for {
		var layer []string
		for _, rel := range []string{
			filepath.Join(".agents", "AGENTS.md"),
			"AGENTS.md",
			"CLAUDE.md",
		} {
			if s := readTrimmed(filepath.Join(dir, rel)); s != "" {
				layer = append(layer, s)
			}
		}
		if len(layer) > 0 {
			layers = append(layers, layer)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	// root-first then deeper so closer files sit later in the prompt
	for i := len(layers) - 1; i >= 0; i-- {
		parts = append(parts, layers[i]...)
	}
	return strings.Join(parts, "\n\n"), nil
}

func readTrimmed(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// ProjectTrusted reports whether the workspace has opted into project-local
// config/skills power. Trust is stored out-of-band under $MOW_HOME (`mow
// trust`) or granted per-invocation via MOW_TRUST_PROJECT=1 — never by a
// marker inside the workspace, which a cloned repo could ship.
func ProjectTrusted(workspace string) bool {
	return config.WorkspaceTrusted(workspace)
}
