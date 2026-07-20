package mow

import (
	"fmt"
	"runtime/debug"
)

// Version is the fallback release string when build info is unavailable.
const Version = "0.1.0"

// VersionString returns a human-readable version for CLI/RPC
// (prefers module version from the binary's build info).
func VersionString() string {
	if bi, ok := debug.ReadBuildInfo(); ok && bi != nil {
		v := bi.Main.Version
		if v != "" && v != "(devel)" {
			return "mow " + v
		}
		// When built as a dependency, Main.Path may not be mow; scan deps.
		for _, d := range bi.Deps {
			if d != nil && d.Path == "github.com/subosito/mow" && d.Version != "" && d.Version != "(devel)" {
				return "mow " + d.Version
			}
		}
	}
	return fmt.Sprintf("mow %s", Version)
}
