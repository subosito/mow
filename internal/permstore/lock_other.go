//go:build !unix

package permstore

import "os"

// lockFile is a no-op where flock is unavailable: the in-process mutex still
// serializes goroutines, and the save stays atomic via rename.
func lockFile(string) (*os.File, error) { return nil, nil }

func unlockFile(*os.File) {}
