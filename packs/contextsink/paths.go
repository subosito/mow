package contextsink

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// pathWithinRoot reports whether path is root or a descendant of root after
// cleaning. It does not follow symlinks; pair with rejectSymlinkComponents
// before open so intermediate directory links cannot escape the root.
func pathWithinRoot(root, path string) bool {
	root = filepath.Clean(root)
	path = filepath.Clean(path)
	if root == "" || path == "" {
		return false
	}
	// Require absolute paths so relative ".." games cannot confuse Rel when
	// the process cwd differs from the session host's view.
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
	// Block ".." and any path that steps above root. Also reject volume-relative
	// escape forms Rel may return on some platforms.
	if rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return false
	}
	if filepath.IsAbs(rel) {
		return false
	}
	return true
}

// rejectSymlinkComponents Lstats every path component from root through path
// and rejects any symlink. Intermediate directory symlinks would otherwise let
// Open follow out of root even when the final name is a regular file.
func rejectSymlinkComponents(root, path string) error {
	root = filepath.Clean(root)
	path = filepath.Clean(path)
	if !pathWithinRoot(root, path) {
		return fmt.Errorf("path escapes root")
	}
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return err
	}
	// Verify root itself is not a symlink to an unexpected place? Root is the
	// session dir the engine owns; Lstat it so a swapped session dir is not
	// followed as a file tree we open through.
	if info, err := os.Lstat(root); err != nil {
		return err
	} else if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("root is a symlink")
	}
	if rel == "." {
		return nil
	}
	cur := root
	for _, part := range strings.Split(rel, string(os.PathSeparator)) {
		if part == "" || part == "." {
			continue
		}
		if part == ".." {
			return fmt.Errorf("path escapes root")
		}
		cur = filepath.Join(cur, part)
		info, err := os.Lstat(cur)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("symlink component")
		}
	}
	return nil
}
