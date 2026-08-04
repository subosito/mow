package config

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// WorkspaceSet is a named collection of directories that share one session:
// a primary Root (the main root of the workspace) plus ExtraRoots
// (additional FS jail roots). Named sets live in $MOW_HOME/workspaces.yaml
// so multi-directory workflows reuse one preset instead of repeated
// --extra-root flags. --workspace / Options.Workspace is hybrid: a set name
// here, or a plain directory path.
type WorkspaceSet struct {
	Root       string   `yaml:"root"`        // main root of this workspace; relative resolves against cwd
	ExtraRoots []string `yaml:"extra_roots"` // additional roots; relative resolves against Root; ":ro" suffix allowed
}

// WorkspaceSetsPath is Home()/workspaces.yaml — the named workspace sets file.
func WorkspaceSetsPath() string {
	return filepath.Join(Home(), "workspaces.yaml")
}

// LoadWorkspaceSets reads all sets from WorkspaceSetsPath. A missing file
// returns an empty map (sets are optional); parse errors surface.
func LoadWorkspaceSets() (map[string]WorkspaceSet, error) {
	path := WorkspaceSetsPath()
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]WorkspaceSet{}, nil
		}
		return nil, fmt.Errorf("workspace sets %s: %w", path, err)
	}
	var f struct {
		Workspaces map[string]WorkspaceSet `yaml:"workspaces"`
	}
	if err := yaml.Unmarshal(raw, &f); err != nil {
		return nil, fmt.Errorf("workspace sets %s: %w", path, err)
	}
	if f.Workspaces == nil {
		f.Workspaces = map[string]WorkspaceSet{}
	}
	return f.Workspaces, nil
}

// LookupWorkspaceSet returns the named set when defined. found=false for a
// missing file or undefined name (callers fall back to treating the argument
// as a directory path); err is only read/parse failure.
func LookupWorkspaceSet(name string) (WorkspaceSet, bool, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return WorkspaceSet{}, false, nil
	}
	sets, err := LoadWorkspaceSets()
	if err != nil {
		return WorkspaceSet{}, false, err
	}
	set, ok := sets[name]
	return set, ok, nil
}

// WorkspaceSetNames returns sorted defined set names (for error messages).
func WorkspaceSetNames() []string {
	sets, err := LoadWorkspaceSets()
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(sets))
	for k := range sets {
		names = append(names, k)
	}
	sort.Strings(names)
	return names
}

// LoadWorkspaceSet reads one named set. Errors name the file and the defined
// sets so typos are fixable without re-reading docs.
func LoadWorkspaceSet(name string) (WorkspaceSet, error) {
	set, found, err := LookupWorkspaceSet(name)
	if err != nil {
		return WorkspaceSet{}, err
	}
	if !found {
		return WorkspaceSet{}, fmt.Errorf("workspace set %q not defined in %s (have: %s)", name, WorkspaceSetsPath(), strings.Join(WorkspaceSetNames(), ", "))
	}
	return set, nil
}

// ResolveWorkspaceSet expands the set into absolute paths: Root against cwd,
// relative ExtraRoots against Root, "~" against the user home. Each returned
// root keeps its optional ":ro"/":rw" suffix so callers can feed it to the
// same SplitExtraRootSpec / splitRootSpecs paths as --extra-root.
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
