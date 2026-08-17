package engine

import (
	"runtime/debug"
	"strings"
)

// Version is the fallback release when the binary has no module/build version.
// Release builds override it with -ldflags "-X …/internal/engine.Version=$VERSION"
// (same string as the repo-root VERSION file and the git tag).
var Version = "1.0.0-rc.1"

// VersionString returns a human-readable version for CLI/RPC
// (prefers module version from the binary's build info).
func VersionString() string {
	if v := strings.TrimSpace(versionFromBuild()); v != "" {
		return "mow " + strings.TrimPrefix(v, "v")
	}
	return "mow " + strings.TrimPrefix(strings.TrimSpace(Version), "v")
}

func versionFromBuild() string {
	bi, ok := debug.ReadBuildInfo()
	if !ok || bi == nil {
		return ""
	}
	if v := bi.Main.Version; v != "" && v != "(devel)" {
		return v
	}
	for _, d := range bi.Deps {
		if d != nil && d.Path == "github.com/subosito/mow" && d.Version != "" && d.Version != "(devel)" {
			return d.Version
		}
	}
	return ""
}

