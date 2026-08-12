package session

import (
	"fmt"
	"os"
	"path/filepath"
)

// openRegularFileNoFollow opens path for read after containment + symlink checks.
func openRegularFileNoFollow(root, path string) (*os.File, error) {
	if err := rejectSymlinkComponents(root, path); err != nil {
		return nil, err
	}
	f, err := openFileNoFollow(path)
	if err != nil {
		return nil, err
	}
	st, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	if st.Mode()&os.ModeSymlink != 0 || !st.Mode().IsRegular() {
		_ = f.Close()
		return nil, fmt.Errorf("not a regular file")
	}
	return f, nil
}

// createRegularFileNoFollow creates path exclusively for write.
func createRegularFileNoFollow(root, path string, perm os.FileMode) (*os.File, error) {
	if err := rejectSymlinkComponents(root, path); err != nil {
		return nil, err
	}
	return createFileNoFollow(path, perm)
}

func toolResultPath(dir, id string) (string, error) {
	if !toolResultIDPattern.MatchString(id) {
		return "", fmt.Errorf("session: invalid tool result id %q", id)
	}
	if dir == "" {
		return "", fmt.Errorf("session: tool result dir unavailable")
	}
	path := filepath.Join(dir, id)
	if err := rejectSymlinkComponents(dir, path); err != nil {
		return "", fmt.Errorf("session: tool result path invalid")
	}
	return path, nil
}
