package tools

import (
	"bufio"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
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

// skipDirNames are never descended into for a recursive glob/grep walk.
// These are dependency and build trees: thousands of files that bury source
// and burn the walk/result budget. A non-recursive pattern is unaffected,
// so an explicit `target/*` or `.git/*` still works.
var skipDirNames = map[string]bool{
	".git":         true,
	"node_modules": true,
	".devenv":      true,
	".direnv":      true,
	"vendor":       true,
	"target":       true, // rust/java build output
	"dist":         true,
	"__pycache__":  true,
	".venv":        true,
	"venv":         true,
	".next":        true,
	".nuxt":        true,
	".turbo":       true,
	".cache":       true,
	"coverage":     true,
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

// lookupFd is exec.LookPath("fd"); tests may stub it.
var lookupFd = func() string {
	for _, name := range []string{"fd", "fdfind"} {
		p, err := exec.LookPath(name)
		if err == nil {
			return p
		}
	}
	return ""
}

func globRelToBase(base, pat string) string {
	b := filepath.ToSlash(base)
	p := filepath.ToSlash(pat)
	if p == b || b == "" {
		return "**"
	}
	prefix := b + "/"
	if strings.HasPrefix(p, prefix) {
		rel := p[len(prefix):]
		if rel == "" {
			return "**"
		}
		return rel
	}
	return p
}

func globFd(ctx context.Context, bin, root, pat string) ([]string, bool) {
	base := walkBase(root, pat)
	rel := globRelToBase(base, pat)
	args := []string{
		"--glob", "--hidden", "--no-ignore", "--absolute-path",
		"--type", "f",
		"--max-results", strconv.Itoa(globMaxMatches + 1),
	}
	for name := range skipDirNames {
		args = append(args, "--exclude", name)
	}
	args = append(args, "--", rel, base)
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = base
	cmd.Stderr = io.Discard
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, false
	}
	if err := cmd.Start(); err != nil {
		return nil, false
	}
	var out []string
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	for sc.Scan() {
		p := strings.TrimSpace(sc.Text())
		if p == "" {
			continue
		}
		out = append(out, p)
		if len(out) > globMaxMatches {
			break
		}
	}
	if len(out) > globMaxMatches && cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
	waitErr := cmd.Wait()
	if len(out) > 0 || ctx.Err() != nil {
		sort.Strings(out)
		if len(out) > globMaxMatches {
			out = out[:globMaxMatches]
		}
		return out, true
	}
	if waitErr == nil {
		return out, true
	}
	var ee *exec.ExitError
	if errors.As(waitErr, &ee) && ee.ExitCode() == 1 {
		return out, true // fd: no matches
	}
	return nil, false
}

// globRecursive walks root and returns paths matching pat (an absolute
// pattern). Results are sorted for stable output.
func globRecursive(root, pat string) ([]string, error) {
	return globWalk(root, pat)
}

func globWalk(root, pat string) ([]string, error) {
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
		if !d.Type().IsRegular() && d.Type()&os.ModeSymlink == 0 {
			return nil // skip sockets/fifos/devices
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
	// Never walk above the containing jail root.
	if root != "" && base != root && !strings.HasPrefix(base+string(os.PathSeparator), root+string(os.PathSeparator)) {
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
