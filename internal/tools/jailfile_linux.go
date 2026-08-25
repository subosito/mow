//go:build linux

package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// fdPath returns the path of the file the kernel has open for f.
// /proc/self/fd/N is bound to the opened inode, so readlink reports the
// path used at open time. Do NOT EvalSymlinks the result: the path string is
// a name, and resolving it again would re-race the filesystem (a hostile
// rename/symlink restore between readlink and EvalSymlinks could make an
// outside inode look in-jail). We Clean and strip a trailing "(deleted)"
// marker. Honest scope: this verifies the recorded open path, not inode
// identity across concurrent renames; full protection comes from the
// descriptor-relative open (openat2 RESOLVE_BENEATH) in jailfile.go.
func fdPath(f *os.File) (string, error) {
	if f == nil {
		return "", fmt.Errorf("nil file")
	}
	link := fmt.Sprintf("/proc/self/fd/%d", f.Fd())
	target, err := os.Readlink(link)
	if err != nil {
		return "", err
	}
	target = strings.TrimSuffix(target, " (deleted)")
	return filepath.Clean(target), nil
}
