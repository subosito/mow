//go:build linux

package tools

import (
	"os"
	"sync"
	"syscall"
	"unsafe"
)

// openat2 (Linux 5.6+) — descriptor-relative jail. RESOLVE_BENEATH makes the
// kernel refuse any component that would escape the root fd (symlink or ..),
// enforced DURING path resolution: no TOCTOU window between check and open.

const (
	sysOpenAT2     = 437 // SYS_openat2 on amd64 and arm64
	resolveBeneath = 0x08
	openHowSize    = 24
)

// rootOpenFlags opens the jail root for descriptor-relative confinement.
// O_DIRECTORY|O_NOFOLLOW: a root swapped for a symlink must not redirect
// the confinement tree (defense in depth; the kernel also refuses non-dirs).
const rootOpenFlags = os.O_RDONLY | syscall.O_DIRECTORY | syscall.O_NOFOLLOW

// openHow mirrors struct open_how (flags, mode, resolve — all u64).
type openHow struct {
	Flags   uint64
	Mode    uint64
	Resolve uint64
}

var (
	openat2Once sync.Once
	openat2OK   bool
)

// openat2Supported probes the kernel once: a valid openat2 call with a bad
// dirfd returns EBADF when the syscall exists, ENOSYS before Linux 5.6.
func openat2Supported() bool {
	openat2Once.Do(func() {
		path, _ := syscall.BytePtrFromString("")
		var how openHow
		_, _, errno := syscall.Syscall6(sysOpenAT2, ^uintptr(0),
			uintptr(unsafe.Pointer(path)), uintptr(unsafe.Pointer(&how)), openHowSize, 0, 0)
		openat2OK = errno != syscall.ENOSYS
	})
	return openat2OK
}

// openatBeneath opens rel under rootFD, refusing any path that escapes the
// root — enforced by the kernel during resolution (RESOLVE_BENEATH), so the
// jail cannot be raced by a concurrent symlink swap.
//
// supported=false (no error) means the kernel lacks openat2 (or seccomp
// blocks it): callers fall back to the legacy check-then-verify open. Escape
// attempts surface as errors (ELOOP/EXDEV/ENOENT).
func openatBeneath(rootFD uintptr, rel string, flag int, perm os.FileMode) (f *os.File, supported bool, err error) {
	if !openat2Supported() {
		return nil, false, nil
	}
	p, err := syscall.BytePtrFromString(rel)
	if err != nil {
		return nil, true, err
	}
	how := openHow{
		Flags:   uint64(flag),
		Mode:    uint64(perm.Perm()),
		Resolve: resolveBeneath,
	}
	fd, _, errno := syscall.Syscall6(sysOpenAT2, rootFD,
		uintptr(unsafe.Pointer(p)), uintptr(unsafe.Pointer(&how)), openHowSize, 0, 0)
	if errno != 0 {
		// ENOSYS (probe raced) or EPERM (seccomp filter) → legacy fallback;
		// both mean "this kernel will not serve openat2", not "escape".
		if errno == syscall.ENOSYS || errno == syscall.EPERM {
			return nil, false, nil
		}
		return nil, true, &os.PathError{Op: "openat2", Path: rel, Err: errno}
	}
	return os.NewFile(fd, rel), true, nil
}
