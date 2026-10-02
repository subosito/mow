package engine

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/subosito/mow/internal/config"
)

// profileDocsRoot creates (when missing) and validates the profile's docs/
// directory. An operator may symlink docs/ elsewhere, but the resolved
// directory must not be the filesystem root or contain the profile directory
// or $MOW_HOME: either would widen the jail to profile config and
// credentials.
func profileDocsRoot(profileDir string) (string, error) {
	docs := filepath.Join(profileDir, "docs")
	if err := os.MkdirAll(docs, 0o700); err != nil {
		return "", fmt.Errorf("workspace docs: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(docs)
	if err != nil {
		return "", fmt.Errorf("workspace docs: %w", err)
	}
	fi, err := os.Stat(resolved)
	if err != nil {
		return "", fmt.Errorf("workspace docs: %w", err)
	}
	if !fi.IsDir() {
		return "", fmt.Errorf("workspace docs: %s is not a directory", docs)
	}
	resolved = filepath.Clean(resolved)
	if resolved == string(filepath.Separator) {
		return "", fmt.Errorf("workspace docs: %s resolves to the filesystem root", docs)
	}
	for _, protected := range []string{profileDir, config.Home()} {
		if protected == "" {
			continue
		}
		p := filepath.Clean(protected)
		if r, err := filepath.EvalSymlinks(p); err == nil {
			p = filepath.Clean(r)
		}
		if containsPath(resolved, p) {
			return "", fmt.Errorf("workspace docs: %s resolves to %s, which would expose %s", docs, resolved, protected)
		}
	}
	return docs, nil
}

// containsPath reports whether child is root or lies beneath it.
func containsPath(root, child string) bool {
	rel, err := filepath.Rel(root, child)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}
