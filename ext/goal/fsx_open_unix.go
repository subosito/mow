//go:build unix

package goal

import (
	"errors"
	"os"
	"syscall"
)

// ErrSymlink is returned when the final component is a symlink.
var ErrSymlink = errors.New("path is a symlink")

// ErrNotRegular is returned for a directory, FIFO, socket, or device.
var ErrNotRegular = errors.New("not a regular file")

// OpenRegular opens path read-only as a regular file without following a final
// symlink (O_NOFOLLOW). Intermediate directory symlinks still resolve —
// callers validate the parent tree when that matters.
func OpenRegular(path string) (*os.File, os.FileInfo, error) {
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		if err == syscall.ELOOP {
			return nil, nil, ErrSymlink
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
		return nil, nil, ErrNotRegular
	}
	return f, st, nil
}

// OpenAppendRegular creates or appends to a regular file without following a
// final symlink (O_NOFOLLOW|O_APPEND|O_CREAT), mode 0600.
func OpenAppendRegular(path string) (*os.File, error) {
	fd, err := syscall.Open(path, syscall.O_WRONLY|syscall.O_APPEND|syscall.O_CREAT|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0o600)
	if err != nil {
		if err == syscall.ELOOP {
			return nil, ErrSymlink
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
		return nil, ErrNotRegular
	}
	return f, nil
}
