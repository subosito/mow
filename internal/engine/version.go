package engine

import (
	"runtime/debug"
	"strings"
)

// Version is the fallback release when the binary has no module/build version.
// Release builds override it with -ldflags "-X …/internal/engine.Version=$VERSION"
// (same string as the repo-root VERSION file and the git tag).
var Version = "1.0.0-rc.1"

// VersionString returns a human-readable version for CLI/RPC.
// A tagged `go install` / module build wins. Untagged checkouts report
// v0.0.0-<timestamp>-<hash> — ignore those so ldflags / VERSION apply.
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
	if v := releaseVersion(bi.Main.Version); v != "" {
		return v
	}
	for _, d := range bi.Deps {
		if d != nil && d.Path == "github.com/subosito/mow" {
			if v := releaseVersion(d.Version); v != "" {
				return v
			}
		}
	}
	return ""
}

// releaseVersion accepts module versions (v1.2.3, v1.0.0-rc.1) and rejects
// "(devel)" and untagged pseudo-versions (v0.0.0-20060102150405-abcdef).
func releaseVersion(v string) string {
	v = strings.TrimSpace(v)
	if v == "" || v == "(devel)" {
		return ""
	}
	if strings.HasPrefix(v, "v0.0.0-") || strings.HasPrefix(v, "0.0.0-") {
		return ""
	}
	return v
}
