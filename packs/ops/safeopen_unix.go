//go:build unix

package ops

import (
	"fmt"
	"os"
	"syscall"
)

// openRegular opens path as a regular file without following a final symlink.
func openRegular(path string) (*os.File, os.FileInfo, error) {
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		if err == syscall.ELOOP {
			return nil, nil, fmt.Errorf("ops: path is a symlink")
		}
		return nil, nil, err
	}
	f := os.NewFile(uintptr(fd), path)
	st, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, nil, err
	}
	if !st.Mode().IsRegular() {
		_ = f.Close()
		return nil, nil, fmt.Errorf("ops: not a regular file")
	}
	return f, st, nil
}
