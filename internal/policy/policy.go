// Package policy enforces workspace path jail and tool allowlists.
package policy

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Policy is the runtime security policy for tool execution.
type Policy struct {
	Workspace    string
	AllowWrite   bool
	AllowShell   bool
	MaxReadBytes int
	// Hashline enables N:hash|line read format and line_hash edits (tools.hashline).
	Hashline bool
}

// Power tools that are denied unless explicitly allowed.
var powerTools = map[string]string{
	"write": "write",
	"edit":  "write",
	"bash":  "shell",
}

// IsPowerTool reports whether name is gated behind allow-write/allow-shell.
// Exported so hosts (approval UIs) share one vocabulary instead of hardcoding
// the list and drifting when a power tool is added.
func IsPowerTool(name string) bool {
	_, ok := powerTools[strings.ToLower(strings.TrimSpace(name))]
	return ok
}

// AllowTool reports whether the named tool may run under this policy.
// Read-only tools always pass the power-tool gate; write/edit/bash need flags.
func (p *Policy) AllowTool(name string) error {
	name = strings.ToLower(strings.TrimSpace(name))
	if kind, ok := powerTools[name]; ok {
		switch kind {
		case "write":
			if !p.AllowWrite {
				return fmt.Errorf("tool %q denied: write disabled (use --allow-write or tools.enable)", name)
			}
		case "shell":
			if !p.AllowShell {
				return fmt.Errorf("tool %q denied: shell disabled (use --allow-shell or tools.enable)", name)
			}
		}
	}
	return nil
}

// ResolvePath joins rel to workspace and ensures the result stays inside workspace.
// Returns absolute cleaned path.
func (p *Policy) ResolvePath(rel string) (string, error) {
	if p.Workspace == "" {
		return "", fmt.Errorf("workspace not set")
	}
	root, err := filepath.Abs(p.Workspace)
	if err != nil {
		return "", err
	}
	root = filepath.Clean(root)

	candidate := rel
	if !filepath.IsAbs(candidate) {
		candidate = filepath.Join(root, candidate)
	}
	candidate = filepath.Clean(candidate)

	// Resolve symlinks so the jail cannot be escaped via a link. EvalSymlinks
	// alone fails on paths that do not exist yet (new-file writes), which would
	// leave a symlinked ancestor — or a dangling symlink final component —
	// unresolved and let a write land outside the workspace.
	resolved, err := resolveSymlinks(candidate, 0)
	if err != nil {
		return "", err
	}
	candidate = resolved
	if resolvedRoot, err := filepath.EvalSymlinks(root); err == nil {
		root = resolvedRoot
	}

	sep := string(filepath.Separator)
	if candidate != root && !strings.HasPrefix(candidate, root+sep) {
		return "", fmt.Errorf("path %q escapes workspace %q", rel, root)
	}
	return candidate, nil
}

const maxSymlinkDepth = 40

// resolveSymlinks is EvalSymlinks that also handles not-yet-existing paths:
// the nearest existing ancestor is resolved and the remainder re-appended, and
// a dangling symlink component is followed manually.
func resolveSymlinks(path string, depth int) (string, error) {
	if depth > maxSymlinkDepth {
		return "", fmt.Errorf("too many symlinks resolving %q", path)
	}
	if r, err := filepath.EvalSymlinks(path); err == nil {
		return r, nil
	}
	dir := filepath.Dir(path)
	if dir == path { // filesystem root
		return path, nil
	}
	rdir, err := resolveSymlinks(dir, depth+1)
	if err != nil {
		return "", err
	}
	full := filepath.Join(rdir, filepath.Base(path))
	if fi, err := os.Lstat(full); err == nil && fi.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(full)
		if err != nil {
			return "", err
		}
		if !filepath.IsAbs(target) {
			target = filepath.Join(rdir, target)
		}
		return resolveSymlinks(target, depth+1)
	}
	return full, nil
}
