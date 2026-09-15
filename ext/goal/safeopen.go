package goal

import (
	"errors"
	"fmt"
	"os"
)

// openRegular opens path as a regular file without following a final symlink.
func openRegular(path string) (*os.File, os.FileInfo, error) {
	f, st, err := OpenRegular(path)
	if err != nil {
		return nil, nil, prefix(err)
	}
	return f, st, nil
}

// openAppendRegular creates or appends a regular file without following a
// final symlink.
func openAppendRegular(path string) (*os.File, error) {
	f, err := OpenAppendRegular(path)
	if err != nil {
		return nil, prefix(err)
	}
	return f, nil
}

// prefix labels this pack's own rejections; OS errors pass through unchanged
// so callers keep os.IsNotExist and the syscall text.
func prefix(err error) error {
	if errors.Is(err, ErrSymlink) || errors.Is(err, ErrNotRegular) {
		return fmt.Errorf("goal: %w", err)
	}
	return err
}
