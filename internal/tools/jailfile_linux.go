//go:build linux

package tools

import (
	"fmt"
	"os"
	"path/filepath"
)

// fdPath returns the path of the file the kernel has open for f.
// /proc/self/fd/N is bound to the opened inode, so a symlink swap after open
// cannot make a hostile fd look like an in-jail path (unlike re-EvalSymlinks
// on f.Name(), which races the other way).
func fdPath(f *os.File) (string, error) {
	if f == nil {
		return "", fmt.Errorf("nil file")
	}
	link := fmt.Sprintf("/proc/self/fd/%d", f.Fd())
	// Readlink gives the path without requiring the path to still exist as a
	// name; EvalSymlinks follows one more step if needed.
	target, err := os.Readlink(link)
	if err != nil {
		return "", err
	}
	// Absolute already on Linux; Clean + EvalSymlinks for consistency with policy.
	target = filepath.Clean(target)
	if r, err := filepath.EvalSymlinks(target); err == nil {
		return r, nil
	}
	return target, nil
}
