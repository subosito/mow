//go:build unix

package goal

import (
	"fmt"
	"os"
	"syscall"
)

// openRegular opens path as a regular file without following a final symlink
// (O_NOFOLLOW). Intermediate directory symlinks still resolve — callers should
// validate the parent tree when that matters.
func openRegular(path string) (*os.File, os.FileInfo, error) {
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		if err == syscall.ELOOP {
			return nil, nil, fmt.Errorf("goal: path is a symlink")
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
		return nil, nil, fmt.Errorf("goal: not a regular file")
	}
	return f, st, nil
}

// openAppendRegular creates or appends a regular file without following a
// final symlink (O_NOFOLLOW|O_APPEND|O_CREAT).
func openAppendRegular(path string) (*os.File, error) {
	fd, err := syscall.Open(path, syscall.O_WRONLY|syscall.O_APPEND|syscall.O_CREAT|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0o600)
	if err != nil {
		if err == syscall.ELOOP {
			return nil, fmt.Errorf("goal: path is a symlink")
		}
		return nil, err
	}
	f := os.NewFile(uintptr(fd), path)
	st, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	if !st.Mode().IsRegular() {
		_ = f.Close()
		return nil, fmt.Errorf("goal: not a regular file")
	}
	return f, nil
}
