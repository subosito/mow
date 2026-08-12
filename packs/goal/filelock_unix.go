//go:build unix

package goal

import (
	"fmt"
	"os"
	"syscall"
)

// lockFileIsAdvisory reports that exclusivity comes from flock, not the path.
func lockFileIsAdvisory() bool { return true }

// tryExclusive opens path and takes a non-blocking exclusive flock.
// The kernel releases the flock when the process dies — no PID required for
// liveness of the lock itself (PID metadata is still written for diagnostics).
func tryExclusive(path string) (*os.File, error) {
	fd, err := syscall.Open(path, syscall.O_RDWR|syscall.O_CREAT|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0o600)
	if err != nil {
		if err == syscall.ELOOP {
			return nil, fmt.Errorf("goal: lock path is a symlink")
		}
		return nil, err
	}
	f := os.NewFile(uintptr(fd), path)
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		if err == syscall.EWOULDBLOCK || err == syscall.EAGAIN {
			return nil, errLocked
		}
		return nil, err
	}
	return f, nil
}

func releaseExclusive(f *os.File) {
	if f == nil {
		return
	}
	_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
}

// processAlive reports whether pid is a live process (signal 0).
// EPERM means the process exists but we cannot signal it — it is alive.
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || err == syscall.EPERM
}
