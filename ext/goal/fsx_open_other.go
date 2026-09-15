//go:build !unix

package goal

import (
	"errors"
	"os"
)

// ErrSymlink is returned when the final component is a symlink.
var ErrSymlink = errors.New("path is a symlink")

// ErrNotRegular is returned for a directory, FIFO, socket, or device.
var ErrNotRegular = errors.New("not a regular file")

func OpenRegular(path string) (*os.File, os.FileInfo, error) {
	st, err := os.Lstat(path)
	if err != nil {
		return nil, nil, err
	}
	if st.Mode()&os.ModeSymlink != 0 {
		return nil, nil, ErrSymlink
	}
	if !st.Mode().IsRegular() {
		return nil, nil, ErrNotRegular
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	return f, st, nil
}

func OpenAppendRegular(path string) (*os.File, error) {
	st, err := os.Lstat(path)
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	if err == nil {
		if st.Mode()&os.ModeSymlink != 0 {
			return nil, ErrSymlink
		}
		if !st.Mode().IsRegular() {
			return nil, ErrNotRegular
		}
	}
	return os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
}
