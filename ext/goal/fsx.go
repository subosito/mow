// Package fsx holds the filesystem helpers several packs need identically:
// containment checks and symlink-refusing opens. They lived once per pack
// (goal, review, ops) and had already started to drift.
package goal

import (
	"os"
	"path/filepath"
	"strings"
)

// WithinRoot reports whether path is root or a descendant of root after
// cleaning. Absolute paths only — a relative root would let cwd decide
// containment. Symlinks are not followed: pair this with an open that refuses
// them (OpenRegular) or with a per-component check.
func WithinRoot(root, path string) bool {
	root = filepath.Clean(root)
	path = filepath.Clean(path)
	if root == "" || path == "" {
		return false
	}
	if !filepath.IsAbs(root) || !filepath.IsAbs(path) {
		return false
	}
	if root == path {
		return true
	}
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return false
	}
	return !filepath.IsAbs(rel)
}
