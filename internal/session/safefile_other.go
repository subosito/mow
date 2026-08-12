//go:build !unix

package session

import (
	"fmt"
	"os"
)

// Portable fallback when O_NOFOLLOW is unavailable: Lstat containment is
// enforced by callers; re-stat after open to reject non-regular files.
func openFileNoFollow(path string) (*os.File, error) {
	f, err := os.Open(path)
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

func createFileNoFollow(path string, perm os.FileMode) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		return nil, err
	}
	st, err := f.Stat()
	if err != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return nil, err
	}
	if st.Mode()&os.ModeSymlink != 0 || !st.Mode().IsRegular() {
		_ = f.Close()
		_ = os.Remove(path)
		return nil, fmt.Errorf("not a regular file")
	}
	return f, nil
}
