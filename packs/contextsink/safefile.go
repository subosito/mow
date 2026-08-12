package contextsink

import (
	"fmt"
	"os"
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
