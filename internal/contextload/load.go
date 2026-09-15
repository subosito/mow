// Package contextload loads AGENTS.md / CLAUDE.md instruction files.
package contextload

import (
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/subosito/mow/internal/config"
)

// Load walks from workspace up to the enclosing boundary (see stopDir)
// collecting instruction files, then
// prepends optional global layers when includeGlobal is true:
// ~/.agents/AGENTS.md (shared base) then $MOW_HOME/AGENTS.md (mow overlay).
func Load(workspace string) (string, error) {
	return LoadWithExtras(workspace)
}

// LoadHermetic is Load without global home files (~/.agents/AGENTS.md and
// $MOW_HOME/AGENTS.md). Used by hermetic Engine construction
// (Options.LoadUserConfig=false) so embedding never pulls operator-home
// instructions. The workspace's own files still load; the ancestor walk stops
// at the workspace so an embedding host never inherits a parent directory's
// AGENTS.md.
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
	stop := stopDir(dir, includeGlobal)
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
		if parent == dir || dir == stop {
			break
		}
		dir = parent
	}
	// root-first then deeper so closer files sit later in the prompt
	for _, layer := range slices.Backward(layers) {
		parts = append(parts, layer...)
	}
	return strings.Join(parts, "\n\n"), nil
}

// stopDir returns the highest directory the instruction walk may read.
// Ancestors above it are someone else's context: walking to / picked up an
// AGENTS.md from an unrelated parent checkout, $HOME, or even /, and fed it to
// the model as project instructions.
//
// Hermetic engines (LoadUserConfig=false) stay in the workspace itself.
// Otherwise the boundary is the nearest enclosing git root (a directory with
// a .git entry) — the repo the workspace belongs to — and $HOME when there is
// none. Never above either: a workspace outside both stops at itself.
func stopDir(workspace string, includeGlobal bool) string {
	if !includeGlobal {
		return workspace
	}
	home, _ := os.UserHomeDir()
	if home != "" {
		if abs, err := filepath.Abs(home); err == nil {
			home = abs
		}
	}
	for dir := workspace; ; {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return dir
		}
		if home != "" && dir == home {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			// Outside $HOME with no git root above: keep to the workspace.
			return workspace
		}
		dir = parent
	}
}

func readTrimmed(path string) string {
	b, err := readFileCapped(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// maxContextFileBytes caps one instruction or skill file. These files go into
// the system prompt verbatim; an oversized (or accidentally binary) file must
// not swallow the context window.
const maxContextFileBytes = 256 << 10

// truncatedMarker is appended so the model (and the operator reading the
// prompt) can see the file was cut rather than silently shortened.
const truncatedMarker = "\n\n[truncated: file exceeds 256 KiB]"

// readFileCapped reads at most maxContextFileBytes of path, appending
// truncatedMarker when the file is larger. The cut lands on a rune boundary.
func readFileCapped(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxContextFileBytes+1))
	if err != nil {
		return nil, err
	}
	if len(b) <= maxContextFileBytes {
		return b, nil
	}
	b = b[:maxContextFileBytes]
	for len(b) > 0 && !utf8.Valid(b[max(0, len(b)-utf8.UTFMax):]) {
		b = b[:len(b)-1]
	}
	return append(b, truncatedMarker...), nil
}

// ProjectTrusted reports whether the workspace has opted into project-local
// config/skills power. Trust is stored out-of-band under $MOW_HOME (`mow
// trust`) or granted per-invocation via MOW_TRUST_PROJECT=1 — never by a
// marker inside the workspace, which a cloned repo could ship.
func ProjectTrusted(workspace string) bool {
	return config.WorkspaceTrusted(workspace)
}
