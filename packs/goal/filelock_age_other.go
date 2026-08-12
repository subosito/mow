//go:build !unix

package goal

import (
	"os"
	"time"
)

// clearStaleLockFileAge removes a lock file on platforms without reliable PID
// probes when metadata shows it is very old.
func clearStaleLockFileAge(path string) bool {
	raw, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	meta := string(raw)
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
