package config

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

type WorkspaceSet struct {
	Root       string   `yaml:"root"`        // main root of this workspace; relative resolves against cwd
	ExtraRoots []string `yaml:"extra_roots"` // additional roots; relative resolves against Root; ":ro" suffix allowed
}

// Profile is a named workspace profile under $MOW_HOME/workspaces/<name>/:
//
//	workspace.yaml  workspace definition (same schema as a workspaces.yaml
//	                set: root + extra_roots; optional — a config-only profile
//	                keeps the caller's default workspace root)
//	config.yaml     optional config overlay, merged above $MOW_HOME/config.yaml
//	                and below explicit --config paths / env / CLI Options
//	                (operator-owned $MOW_HOME state; may set anything a global
//	                config.yaml may)
//	AGENTS.md       optional profile instructions
//	skills/         optional profile skills
//	plugins/        optional profile Agent Plugins
//
// Profiles replace the single flat $MOW_HOME/workspaces.yaml registry: each
// profile is self-contained, and per-profile config is what makes ACP
// agents/mow_agents profile-scoped without touching ext/acp (the overlay is
// applied before extensions decode extensions.acp).
type Profile struct {
	// Name is the profile name (the directory under WorkspacesDir).
	Name string
	// Dir is the absolute profile directory.
	Dir string
	// WorkspaceSet carries root + extra_roots from workspace.yaml (zero
	// value when the file is absent: Root == "" means "keep default root").
	WorkspaceSet
}

// WorkspacesDir is Home()/workspaces — the profile registry directory.
func WorkspacesDir() string {
	return filepath.Join(Home(), "workspaces")
}

// ProfileDir is the directory of one named profile.
func ProfileDir(name string) string {
	return filepath.Join(WorkspacesDir(), name)
}

// validProfileName guards against path traversal: a profile name must be a
// single plain directory component with no surrounding whitespace.
func validProfileName(name string) bool {
	if name == "" || name != strings.TrimSpace(name) {
		return false
	}
	if name == "." || name == ".." {
		return false
	}
	if strings.ContainsAny(name, "/\\") || strings.ContainsRune(name, 0) {
		return false
	}
	return filepath.Base(name) == name
}

// IsWorkspaceProfileName reports whether a --workspace argument is a valid
// profile name (syntactically), independent of whether the profile exists.
func IsWorkspaceProfileName(name string) bool { return validProfileName(name) }

// LoadProfile looks up one profile by name. found=false when the directory
// does not exist or the argument is not a valid profile name (the caller
// falls back to treating the argument as a directory path); err is only a
// read or parse failure of an existing profile.
func LoadProfile(name string) (Profile, bool, error) {
	if !validProfileName(name) {
		return Profile{}, false, nil
	}
	dir := ProfileDir(name)
	fi, err := os.Stat(dir)
	if err != nil || !fi.IsDir() {
		return Profile{}, false, nil
	}
	p := Profile{Name: name, Dir: dir}
	wsPath := filepath.Join(dir, "workspace.yaml")
	raw, rerr := os.ReadFile(wsPath)
	switch {
	case rerr == nil:
		var ws WorkspaceSet
		if uerr := yaml.Unmarshal(raw, &ws); uerr != nil {
			return Profile{}, false, fmt.Errorf("workspace profile %s: %w", wsPath, uerr)
		}
		p.WorkspaceSet = ws
	case os.IsNotExist(rerr):
		// config-only profile: keep the caller's default workspace root.
	default:
		return Profile{}, false, fmt.Errorf("workspace profile %s: %w", wsPath, rerr)
	}
	return p, true, nil
}

// ListProfiles returns the sorted profile names (subdirectories of
// WorkspacesDir). A missing registry directory returns an empty list.
func ListProfiles() ([]string, error) {
	ents, err := os.ReadDir(WorkspacesDir())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var names []string
	for _, e := range ents {
		if e.IsDir() && validProfileName(e.Name()) {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	return names, nil
}

// ConfigPath is the profile config overlay path (may not exist).
func (p Profile) ConfigPath() string { return filepath.Join(p.Dir, "config.yaml") }

// HasConfig reports whether the profile ships a config.yaml overlay.
func (p Profile) HasConfig() bool {
	fi, err := os.Stat(p.ConfigPath())
	return err == nil && !fi.IsDir()
}

// AgentsPath is the profile AGENTS.md path (may not exist).
func (p Profile) AgentsPath() string { return filepath.Join(p.Dir, "AGENTS.md") }

// HasAgents reports whether the profile ships an AGENTS.md.
func (p Profile) HasAgents() bool {
	fi, err := os.Stat(p.AgentsPath())
	return err == nil && !fi.IsDir()
}

// SkillsDir is the profile skills directory (may not exist).
func (p Profile) SkillsDir() string { return filepath.Join(p.Dir, "skills") }

// HasSkills reports whether the profile ships a skills directory.
func (p Profile) HasSkills() bool {
	fi, err := os.Stat(p.SkillsDir())
	return err == nil && fi.IsDir()
}

// PluginsDir is the profile plugins directory (may not exist).
func (p Profile) PluginsDir() string { return filepath.Join(p.Dir, "plugins") }

// HasPlugins reports whether the profile ships a plugins directory.
func (p Profile) HasPlugins() bool {
	fi, err := os.Stat(p.PluginsDir())
	return err == nil && fi.IsDir()
}

// OverlayConfigPaths prepends the profile config overlay path to explicit
// config paths for callers that only have a path list (e.g. ext.BeforeNew →
// RegisterFromConfig). The overlay path is included even when the file is
// absent so path-only plugin discovery can still see
// $MOW_HOME/workspaces/<name>/plugins/. Loaders must skip missing files.
// With Load's file order (global < paths), putting the profile first among
// paths yields:
//
//	global < profile config.yaml < explicit --config paths
//
// Prefer LoadWithProfile(name, explicitPaths) when the profile name is known;
// this helper exists for path-only hooks that cannot pass a profile name.
func (p Profile) OverlayConfigPaths(paths []string) []string {
	cfg := p.ConfigPath()
	if strings.TrimSpace(cfg) == "" {
		return paths
	}
	want := filepath.Clean(cfg)
	for _, existing := range paths {
		if filepath.Clean(strings.TrimSpace(existing)) == want {
			return paths
		}
	}
	out := make([]string, 0, len(paths)+1)
	out = append(out, cfg)
	return append(out, paths...)
}

func (s WorkspaceSet) ResolveWorkspaceSet() (workspace string, roots []string, err error) {
	ws := expandHome(strings.TrimSpace(s.Root))
	if ws == "" {
		return "", nil, fmt.Errorf("workspace set: empty root")
	}
	if !filepath.IsAbs(ws) {
		abs, aerr := filepath.Abs(ws)
		if aerr != nil {
			return "", nil, fmt.Errorf("workspace set: %w", aerr)
		}
		ws = abs
	}
	ws = filepath.Clean(ws)
	if fi, serr := os.Stat(ws); serr != nil || !fi.IsDir() {
		return "", nil, fmt.Errorf("workspace set: root %q is not a directory", ws)
	}
	roots = make([]string, 0, len(s.ExtraRoots))
	for _, raw := range s.ExtraRoots {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		path, suffix := splitRootSuffix(raw)
		path = expandHome(path)
		if !filepath.IsAbs(path) {
			path = filepath.Join(ws, path)
		}
		path = filepath.Clean(path)
		roots = append(roots, path+suffix)
	}
	return ws, roots, nil
}

// splitRootSuffix separates an optional ":ro"/":rw" suffix from a path
// (case-insensitive; ":rw" normalizes away).

func splitRootSuffix(raw string) (path, suffix string) {
	lower := strings.ToLower(raw)
	switch {
	case strings.HasSuffix(lower, ":ro"):
		return strings.TrimSpace(raw[:len(raw)-3]), ":ro"
	case strings.HasSuffix(lower, ":rw"):
		return strings.TrimSpace(raw[:len(raw)-3]), ""
	default:
		return raw, ""
	}
}

// expandHome expands a leading "~" or "~/..." against the user home dir.
func expandHome(p string) string {
	if p == "~" || strings.HasPrefix(p, "~"+string(os.PathSeparator)) {
		if h, err := os.UserHomeDir(); err == nil {
			if p == "~" {
				return h
			}
			return filepath.Join(h, p[2:])
		}
	}
	return p
}
