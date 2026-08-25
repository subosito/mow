//go:build !linux

package tools

import "os"

// rootOpenFlags off-Linux: plain read-only open (the kernel fast path is
// unavailable here anyway; openatBeneath always reports unsupported).
const rootOpenFlags = os.O_RDONLY

// openatBeneath is unavailable off-Linux: the legacy check-then-verify open
// (jailfile.go) applies. On darwin the post-open check uses fcntl F_GETPATH
// (handle-based), which covers the common swap-after-open case.
func openatBeneath(rootFD uintptr, rel string, flag int, perm os.FileMode) (*os.File, bool, error) {
	return nil, false, nil
}
