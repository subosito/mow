//go:build !unix

package contextsink

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
