//go:build !unix

package goal

import (
	"os"
)

// lockFileIsAdvisory is false: the directory entry itself is the lock.
func lockFileIsAdvisory() bool { return false }

// tryExclusive uses O_EXCL create so only one process owns the lock file.
// Stale recovery is PID-based (see clearStaleLockFile).
func tryExclusive(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err != nil {
		if os.IsExist(err) {
			return nil, errLocked
		}
		return nil, err
	}
	return f, nil
}

func releaseExclusive(f *os.File) {
	// Ownership is the file itself; Release removes it after Close.
}

// processAlive is best-effort without signals: treat unknown as alive so we
// never steal a lock from a running peer. Operators can delete the lock file.
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	// FindProcess always succeeds on many platforms; we cannot probe liveness
	// portably without kill(0). Fail closed: assume alive.
	_, err := os.FindProcess(pid)
	return err == nil
}
