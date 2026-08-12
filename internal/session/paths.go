package session

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
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

// rejectSymlinkComponents Lstats every component from root through path and
// rejects symlinks (intermediate dir symlinks would escape on open).
func rejectSymlinkComponents(root, path string) error {
	root = filepath.Clean(root)
	path = filepath.Clean(path)
	if !pathWithinRoot(root, path) {
		return fmt.Errorf("path escapes root")
	}
	if info, err := os.Lstat(root); err != nil {
		return err
	} else if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("root is a symlink")
	}
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return err
	}
	if rel == "." {
		return nil
	}
	cur := root
	parts := strings.Split(rel, string(os.PathSeparator))
	for i, part := range parts {
		if part == "" || part == "." {
			continue
		}
		if part == ".." {
			return fmt.Errorf("path escapes root")
		}
		cur = filepath.Join(cur, part)
		info, err := os.Lstat(cur)
		if err != nil {
			if os.IsNotExist(err) && i == len(parts)-1 {
				return nil
			}
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("symlink component")
		}
	}
	return nil
}
