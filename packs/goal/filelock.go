package goal

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// errLocked is returned when a non-blocking exclusive lock cannot be taken.
var errLocked = errors.New("resource locked")

// fileLock is an exclusive lock on a path, held for the process lifetime of f.
type fileLock struct {
	path string
	f    *os.File
}

// Release drops the exclusive lock. Idempotent.
func (l *fileLock) Release() {
	if l == nil || l.f == nil {
		return
	}
	if !lockFileIsAdvisory() {
		// O_EXCL platforms own the directory entry. Unlink BEFORE close:
		// removing after close could hit a lock file another process
		// already recreated, deleting a live peer's lock entry.
		_ = os.Remove(l.path)
	}
	releaseExclusive(l.f)
	_ = l.f.Close()
	l.f = nil
	// flock platforms must leave the inode in place: unlinking a live
	// flocked file lets a waiter create a second inode and take a second
	// exclusive lock.
}

const defaultLockWait = 30 * time.Second

// acquireFileLock takes an exclusive lock on path (creating parent dirs as needed).
// When block is false, returns errLocked if another holder exists.
// Stale lock files are reclaimed only on O_EXCL platforms; flock is kernel-owned.
func acquireFileLock(path string, block bool) (*fileLock, error) {
	return acquireFileLockWait(context.Background(), path, block, defaultLockWait)
}

func acquireFileLockWait(ctx context.Context, path string, block bool, wait time.Duration) (*fileLock, error) {
	path = filepath.Clean(path)
	if path == "" || path == "." {
		return nil, fmt.Errorf("goal: empty lock path")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	var deadline time.Time
	if wait > 0 {
		deadline = time.Now().Add(wait)
	}
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		f, err := tryExclusive(path)
		if err == nil {
			_ = writeLockOwner(f)
			return &fileLock{path: path, f: f}, nil
		}
		if !errors.Is(err, errLocked) {
			return nil, err
		}
		// PID/age reclaim unlinks the path. That is correct only when the
		// directory entry *is* the lock (O_EXCL). Doing it under flock
		// splits the lock across two inodes.
		if !lockFileIsAdvisory() {
			if clearStaleLockFile(path) {
				continue
			}
			if clearStaleLockFileAge(path) {
				continue
			}
		}
		if !block {
			return nil, errLocked
		}
		if !deadline.IsZero() && time.Now().After(deadline) {
			return nil, fmt.Errorf("%w: timeout waiting for %s", errLocked, path)
		}
		timer := time.NewTimer(25 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

func writeLockOwner(f *os.File) error {
	if f == nil {
		return nil
	}
	_ = f.Truncate(0)
	_, err := f.Seek(0, 0)
	if err != nil {
		return err
	}
	host, _ := os.Hostname()
	_, err = fmt.Fprintf(f, "pid=%d\nhost=%s\nats=%s\n",
		os.Getpid(), host, time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		return err
	}
	return f.Sync()
}

// staleLockMaxAge is how old a lock file may be before !unix platforms may
// reclaim it when PID liveness cannot be probed portably.
const staleLockMaxAge = 72 * time.Hour

// maxLockMetaBytes bounds PID/host metadata reads so a planted lock file
// cannot be ReadFile'd unbounded during stale reclaim.
const maxLockMetaBytes = 4096

func readLockMeta(path string) (string, bool) {
	f, err := os.Open(path)
	if err != nil {
		return "", false
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, maxLockMetaBytes))
	if err != nil {
		return "", false
	}
	return string(raw), true
}

// clearStaleLockFile removes path when it appears to be an abandoned lock
// (owner PID dead or unreadable). Returns true if the file was removed.
func clearStaleLockFile(path string) bool {
	meta, ok := readLockMeta(path)
	if !ok {
		return false
	}
	pid := parseLockPID(meta)
	host := parseLockHost(meta)
	currentHost, _ := os.Hostname()
	if host != "" && currentHost != "" && host != currentHost {
		// Another host may still hold the lock (or NFS copy); do not steal.
		return false
	}
	if pid > 0 && processAlive(pid) {
		return false
	}
	if pid <= 0 {
		return false
	}
	return os.Remove(path) == nil
}

func parseLockPID(s string) int {
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "pid=") {
			n, _ := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(line, "pid=")))
			return n
		}
		// bare pid line (legacy / simple lockers)
		if n, err := strconv.Atoi(line); err == nil && n > 0 {
			return n
		}
	}
	return 0
}

func parseLockHost(s string) string {
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "host=") {
			return strings.TrimSpace(strings.TrimPrefix(line, "host="))
		}
	}
	return ""
}

func parseLockTime(s string) (time.Time, bool) {
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "ats=") {
			t, err := time.Parse(time.RFC3339, strings.TrimSpace(strings.TrimPrefix(line, "ats=")))
			if err == nil {
				return t, true
			}
		}
	}
	return time.Time{}, false
}
