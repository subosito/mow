//go:build darwin

package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"unsafe"
)

// fdPath on darwin resolves the fd itself via F_GETPATH (the kernel reports
// the path bound to the open descriptor), not the name recorded at open time.
// A rename or symlink swap AFTER open cannot redirect the check the way
// re-EvalSymlinks(f.Name()) could.
//
// Honesty bounds: F_GETPATH returns a current path for the vnode (it can
// differ from the open-time path if the file was renamed); a hardlink
// outside the jail is still possible because the kernel reports one name
// for the inode. This closes the open-time-race gap the name-based fallback
// leaves open; it is not inode-perfection.
func fdPath(f *os.File) (string, error) {
	if f == nil {
		return "", fmt.Errorf("nil file")
	}
	// MAXPATHLEN on darwin is 1024.
	buf := make([]byte, 1024)
	_, _, errno := syscall.Syscall(syscall.SYS_FCNTL, f.Fd(), syscall.F_GETPATH, uintptr(unsafe.Pointer(&buf[0])))
	if errno != 0 {
		return "", fmt.Errorf("fcntl F_GETPATH fd %d: %w", f.Fd(), errno)
	}
	// F_GETPATH writes a NUL-terminated path.
	n := 0
	for n < len(buf) && buf[n] != 0 {
		n++
	}
	p := string(buf[:n])
	if p == "" {
		return "", fmt.Errorf("fcntl F_GETPATH fd %d: empty path", f.Fd())
	}
	return filepath.Clean(p), nil
}
