//go:build !linux && !darwin

package tools

import (
	"fmt"
	"os"
	"path/filepath"
)

// fdPath best-effort on platforms with no handle-based lookup (no
// /proc/self/fd, no F_GETPATH): re-resolve f.Name(). This catches the
// common "open followed a symlink that still points outside" case, but is
// weaker than Linux /proc/self/fd or darwin F_GETPATH — a swap *back*
// inside between open and this check can still pass while the fd
// references an outside inode.
func fdPath(f *os.File) (string, error) {
	if f == nil {
		return "", fmt.Errorf("nil file")
	}
	name := f.Name()
	if name == "" {
		return "", fmt.Errorf("empty file name")
	}
	if r, err := filepath.EvalSymlinks(name); err == nil {
		return r, nil
	}
	// File may not exist under name anymore; return cleaned name for the
	// caller to jail-check (fail closed if outside).
	abs, err := filepath.Abs(name)
	if err != nil {
		return "", err
	}
	return filepath.Clean(abs), nil
}
