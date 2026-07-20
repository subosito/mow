// Package contextload loads AGENTS.md / CLAUDE.md instruction files.
package contextload

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/subosito/mow/internal/config"
)

// Load walks from workspace up to root collecting AGENTS.md and CLAUDE.md,
// then prepends optional global $MOW_HOME/AGENTS.md (default ~/.mow/AGENTS.md).
func Load(workspace string) (string, error) {
	var parts []string
	if b, err := os.ReadFile(config.AgentsPath()); err == nil {
		if s := strings.TrimSpace(string(b)); s != "" {
			parts = append(parts, s)
		}
	}
	dir, err := filepath.Abs(workspace)
	if err != nil {
		return "", err
	}
	var chain []string
	for {
		for _, name := range []string{"AGENTS.md", "CLAUDE.md"} {
			p := filepath.Join(dir, name)
			b, err := os.ReadFile(p)
			if err != nil {
				continue
			}
			if s := strings.TrimSpace(string(b)); s != "" {
				chain = append(chain, s)
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	// root-first then deeper: reverse chain so closer files win later in prompt
	for i := len(chain) - 1; i >= 0; i-- {
		parts = append(parts, chain[i])
	}
	return strings.Join(parts, "\n\n"), nil
}
