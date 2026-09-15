//go:build unix

package permstore

import (
	"os"
	"syscall"
)

// lockFile takes an exclusive advisory lock on path, creating it if needed.
// The rules file is shared by every mow process pointed at the same
// $MOW_HOME, so read-modify-write must be serialized across processes, not
// only across goroutines: two concurrent Remember calls would otherwise each
// load the same list and the later rename would drop the other's rule.
// The lock lives on a sidecar so it survives the rename that replaces the
// rules file.
func lockFile(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		_ = f.Close()
		return nil, err
	}
	return f, nil
}

func unlockFile(f *os.File) {
	if f == nil {
		return
	}
	_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	_ = f.Close()
}
