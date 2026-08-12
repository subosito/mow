package lsp

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/subosito/mow"
)

// pathWithinRoot reports whether path is root or a descendant of root.
// Both paths must be absolute so relative ".." games cannot depend on cwd.
func pathWithinRoot(root, path string) bool {
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
	if filepath.IsAbs(rel) {
		return false
	}
	return true
}

// resolvePath maps a tool path under the configured LSP root (tests and hosts
// without an engine in context).
func resolvePath(root, path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", fmt.Errorf("lsp: path required")
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("lsp: server root: %w", err)
	}
	var abs string
	if filepath.IsAbs(path) {
		abs = filepath.Clean(path)
	} else {
		abs, err = filepath.Abs(filepath.Join(rootAbs, path))
		if err != nil {
			return "", fmt.Errorf("lsp: path: %w", err)
		}
	}
	if !pathWithinRoot(rootAbs, abs) {
		return "", fmt.Errorf("lsp: path %q outside server root", path)
	}
	return abs, nil
}

// resolvePathFromEngine applies the same path jail as FS tools when an engine
// is available; otherwise resolvePath against the configured LSP root.
func resolvePathFromEngine(ctx context.Context, root, path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", fmt.Errorf("lsp: path required")
	}
	if eng := mow.EngineFromContext(ctx); eng != nil {
		abs, err := eng.ResolvePath(path)
		if err != nil {
			return "", fmt.Errorf("lsp: %w", err)
		}
		return abs, nil
	}
	return resolvePath(root, path)
}
