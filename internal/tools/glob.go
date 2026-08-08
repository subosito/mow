package tools

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Recursive glob (`**`).
//
// Go's filepath.Glob has no recursive wildcard. It splits the pattern on
// separators and matches each segment independently, so `**` behaves exactly
// like `*` — one directory level, no more. That makes the documented
// `**/*.go` silently wrong in two ways: it misses anything deeper than one
// level, and it misses files at the root entirely, because `**/x` requires a
// leading directory segment to exist.
//
// The fix is a segment matcher where `**` consumes zero or more path
// components, which is the convention every other tool (git, ripgrep, bash
// globstar, editorconfig) uses.

// globWalkLimit bounds the directory walk. A recursive pattern on a large
// tree is unbounded work, and the model only ever sees globMaxMatches results
// anyway. Stopping early keeps a stray `**/*` from stalling the turn.
const globWalkLimit = 200_000

// skipDirNames are never descended into for a recursive pattern. These hold
// build output and dependency trees: thousands of files that bury the handful
// the model actually wants, and on this repo .devenv alone dwarfs the source.
// A non-recursive pattern is unaffected, so an explicit `.git/*` still works.
var skipDirNames = map[string]bool{
	".git":         true,
	"node_modules": true,
	".devenv":      true,
	".direnv":      true,
	"vendor":       true,
}

// hasDoublestar reports whether pat contains a ** path segment.
func hasDoublestar(pat string) bool {
	for _, seg := range strings.Split(filepath.ToSlash(pat), "/") {
		if seg == "**" {
			return true
		}
	}
	return false
}

// globRecursive walks root and returns paths matching pat (an absolute
// pattern). Results are sorted for stable output.
func globRecursive(root, pat string) ([]string, error) {
	pat = filepath.ToSlash(pat)
	// Anchor the walk at the deepest literal prefix so `internal/**/x.go`
	// does not walk the whole workspace.
	base := walkBase(root, pat)
	segs := strings.Split(pat, "/")

	var out []string
	seen := 0
	err := filepath.WalkDir(base, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			// Unreadable subtree: skip it rather than failing the whole glob.
			if d != nil && d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if seen++; seen > globWalkLimit {
			return filepath.SkipAll
		}
		if d.IsDir() {
			if path != base && skipDirNames[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if matchSegments(segs, strings.Split(filepath.ToSlash(path), "/")) {
			out = append(out, path)
			if len(out) > globMaxMatches {
				return filepath.SkipAll
			}
		}
		return nil
	})
	if err != nil && err != filepath.SkipAll {
		return nil, err
	}
	sort.Strings(out)
	return out, nil
}

// walkBase returns the longest leading run of literal (wildcard-free) segments
// of pat, so the walk starts as deep as possible. Falls back to root.
func walkBase(root, pat string) string {
	segs := strings.Split(pat, "/")
	var lit []string
	for _, s := range segs {
		if strings.ContainsAny(s, "*?[") {
			break
		}
		lit = append(lit, s)
	}
	if len(lit) == 0 {
		return root
	}
	// Drop the final literal when it is the filename itself, so the parent
	// directory is walked rather than a non-directory path.
	if len(lit) == len(segs) {
		lit = lit[:len(lit)-1]
	}
	base := strings.Join(lit, "/")
	if base == "" {
		return root
	}
	if fi, err := os.Stat(base); err != nil || !fi.IsDir() {
		return root
	}
	// Never walk above the workspace root, whatever the pattern claims.
	if !strings.HasPrefix(base+"/", root+"/") && base != root {
		return root
	}
	return base
}

// matchSegments reports whether name segments match pattern segments, with
// `**` consuming zero or more segments.
//
// Zero-or-more is the important half: it is what lets `**/sample.png` match a
// file sitting at the root, which is the case that failed before.
func matchSegments(pat, name []string) bool {
	// Empty pattern matches only an empty name.
	if len(pat) == 0 {
		return len(name) == 0
	}
	if pat[0] == "**" {
		// Trailing ** matches everything that remains.
		if len(pat) == 1 {
			return true
		}
		// Try consuming 0, 1, 2, … leading name segments.
		for i := 0; i <= len(name); i++ {
			if matchSegments(pat[1:], name[i:]) {
				return true
			}
		}
		return false
	}
	if len(name) == 0 {
		return false
	}
	ok, err := filepath.Match(pat[0], name[0])
	if err != nil || !ok {
		return false
	}
	return matchSegments(pat[1:], name[1:])
}
