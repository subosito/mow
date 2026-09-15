//go:build unix

package goal

// clearStaleLockFileAge is a no-op on Unix where kill(0) probes liveness.
func clearStaleLockFileAge(path string) bool { return false }
