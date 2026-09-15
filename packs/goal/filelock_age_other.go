//go:build !unix

package goal

import (
	"os"
	"strings"
	"time"
)

// staleLockMaxAge is how old a lock file may be before !unix platforms may
// reclaim it when PID liveness cannot be probed portably.
const staleLockMaxAge = 72 * time.Hour

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

// clearStaleLockFileAge removes a lock file on platforms without reliable PID
// probes when metadata shows it is very old.
func clearStaleLockFileAge(path string) bool {
	meta, ok := readLockMeta(path)
	if !ok {
		return false
	}
	host := parseLockHost(meta)
	currentHost, _ := os.Hostname()
	if host != "" && currentHost != "" && host != currentHost {
		return false
	}
	if at, ok := parseLockTime(meta); ok && time.Since(at) < staleLockMaxAge {
		return false
	}
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	if time.Since(info.ModTime()) < staleLockMaxAge {
		return false
	}
	return os.Remove(path) == nil
}
