// Package policy enforces workspace path jail and tool allowlists.
package policy

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/subosito/mow/internal/sandbox"
)

// JailRoot is one absolute directory tree permitted by the path jail.
type JailRoot struct {
	Path     string
	ReadOnly bool
}

// Policy is the runtime security policy for tool execution.
type Policy struct {
	Workspace string
	// ExtraRoots are additional absolute directory trees FS tools may touch
	// (same symlink rules as Workspace). Set only from user/host config or CLI
	// — never from project .mow/config.
	ExtraRoots []string
	// ExtraRootsReadOnly are additional absolute directory trees FS tools may
	// read from, but write/edit operations are denied even when AllowWrite is true.
	ExtraRootsReadOnly []string
	// WritableRoots is an explicit allowlist of absolute directory trees where
	// write/edit is permitted even when ReadOnly is true (the primary workspace
	// is read-only under ReadOnly). Entries come from --extra-root PATH:rw under
	// --read-only. Under normal (non-read-only) operation this is empty and
	// irrelevant — AllowWrite governs the tool gate and every jail root is
	// writable. This is an allowlist, not deny logic: a root is writable only
	// when it is the most-specific match AND appears in this list while
	// ReadOnly holds.
	WritableRoots []string
	// ReadOnly makes the primary workspace a read-only jail root. Unlike
	// AllowWrite=false (which removes the write/edit tools entirely), ReadOnly
	// keeps the tools available so writes can still land under a WritableRoots
	// entry. Set by --read-only when at least one --extra-root PATH:rw is given.
	ReadOnly   bool
	AllowWrite bool
	AllowShell bool
	// Sandbox is the opt-in OS jail for shell execution (bash + proc_start).
	// Empty / "none" = today's behavior: --allow-shell runs an unsandboxed
	// `bash -lc` as the user. "bwrap" wraps both shell entry points in
	// bubblewrap (Linux only). Ignored entirely when AllowShell is false —
	// there is no shell to jail. Carried on the policy so tools never re-read
	// flags or config.
	Sandbox sandbox.Mode
	// sandboxOnce/backend memoize the resolved backend: building it does a
	// LookPath and (for bwrap) validates the platform, and every bash call
	// would otherwise repeat that.
	sandboxOnce sync.Once
	sandboxBE   sandbox.Backend
	sandboxErr  error

	MaxReadBytes int
	// BashTimeoutSec caps each bash tool Exec (default 300). Soft-returns on timeout.
	BashTimeoutSec int
	// MaxBashTimeoutSec bounds a per-call timeout_sec request (default 900).
	MaxBashTimeoutSec int
	// Hashline enables N:hash|line read format and line_hash edits (tools.hashline).
	Hashline bool
}

// Power tools that are denied unless explicitly allowed.
var powerTools = map[string]string{
	"write":      "write",
	"edit":       "write",
	"bash":       "shell",
	"proc_start": "shell",
	"proc_stop":  "shell",
}

// IsPowerTool reports whether name is gated behind allow-write/allow-shell.
// Exported so hosts (approval UIs) share one vocabulary instead of hardcoding
// the list and drifting when a power tool is added.
func IsPowerTool(name string) bool {
	_, ok := powerTools[strings.ToLower(strings.TrimSpace(name))]
	return ok
}

// AllowTool reports whether the named tool may run under this policy.
// Read-only tools always pass the power-tool gate; write/edit/bash need flags.
func (p *Policy) AllowTool(name string) error {
	name = strings.ToLower(strings.TrimSpace(name))
	if kind, ok := powerTools[name]; ok {
		switch kind {
		case "write":
			if !p.AllowWrite {
				return fmt.Errorf("tool %q denied: write disabled (use --allow-write or tools.enable)", name)
			}
		case "shell":
			if !p.AllowShell {
				return fmt.Errorf("tool %q denied: shell disabled (use --allow-shell or tools.enable)", name)
			}
		}
	}
	return nil
}

// ResolvePath joins rel to workspace (when relative) and ensures the result
// stays inside the workspace or an ExtraRoot. Returns absolute cleaned path.
func (p *Policy) ResolvePath(rel string) (string, error) {
	return p.ResolvePathFor(rel, false)
}

// ResolvePathFor checks path jail boundaries for relative or absolute paths.
// When write is true, paths matching a read-only root are rejected.
func (p *Policy) ResolvePathFor(rel string, write bool) (string, error) {
	if p == nil || p.Workspace == "" {
		return "", fmt.Errorf("workspace not set")
	}
	roots, err := p.jailRoots()
	if err != nil {
		return "", err
	}
	if len(roots) == 0 {
		return "", fmt.Errorf("workspace not set")
	}
	primary := roots[0].Path

	candidate := rel
	if !filepath.IsAbs(candidate) {
		candidate = filepath.Join(primary, candidate)
	}
	candidate = filepath.Clean(candidate)

	// Resolve symlinks so the jail cannot be escaped via a link. EvalSymlinks
	// alone fails on paths that do not exist yet (new-file writes), which would
	// leave a symlinked ancestor — or a dangling symlink final component —
	// unresolved and let a write land outside the workspace.
	resolved, err := resolveSymlinks(candidate, 0)
	if err != nil {
		return "", err
	}
	candidate = resolved

	sep := string(filepath.Separator)
	var matchedRoot *JailRoot
	var longestLen int

	for i := range roots {
		r := roots[i].Path
		if resolvedRoot, err := filepath.EvalSymlinks(r); err == nil {
			r = filepath.Clean(resolvedRoot)
		}
		// Re-check after symlink resolution: a root that resolves to "/" would
		// otherwise match every absolute path via a broken or overly broad prefix.
		if r == string(filepath.Separator) {
			return "", fmt.Errorf("%s resolves to filesystem root (not allowed as path jail root)", roots[i].Path)
		}
		if candidate == r || strings.HasPrefix(candidate, r+sep) {
			// Longest-prefix / most-specific root match wins.
			if len(r) > longestLen {
				longestLen = len(r)
				matchedRoot = &roots[i]
			} else if len(r) == longestLen && roots[i].ReadOnly {
				// Equal path lengths: read-only deny wins over read-write.
				matchedRoot = &roots[i]
			}
		}
	}

	if matchedRoot != nil {
		if write && matchedRoot.ReadOnly {
			return "", fmt.Errorf("path %q resolves under read-only root %q", rel, matchedRoot.Path)
		}
		return candidate, nil
	}
	return "", fmt.Errorf("path %q escapes workspace (and extra roots)", rel)
}

// Beneath maps an absolute path to the jail root containing it, using the
// same root selection as ResolvePathFor (longest match, read-only tie-break),
// and returns the root's canonical (symlink-resolved) directory plus the path
// relative to it. The pair is meant for descriptor-relative open (openat2
// RESOLVE_BENEATH), where the KERNEL enforces confinement during resolution.
// ok=false when the path is not under any root: callers fall back to the
// legacy check-then-verify open.
func (p *Policy) Beneath(abs string) (root, rel string, ok bool) {
	if p == nil || p.Workspace == "" {
		return "", "", false
	}
	roots, err := p.jailRoots()
	if err != nil || len(roots) == 0 {
		return "", "", false
	}
	candidate := filepath.Clean(abs)
	if !filepath.IsAbs(candidate) {
		return "", "", false
	}
	sep := string(filepath.Separator)
	var matchedLen int
	var canonical string
	for i := range roots {
		r := roots[i].Path
		if resolvedRoot, err := filepath.EvalSymlinks(r); err == nil {
			r = filepath.Clean(resolvedRoot)
		}
		if r == string(filepath.Separator) {
			continue
		}
		if candidate == r || strings.HasPrefix(candidate, r+sep) {
			// Longest-prefix / most-specific root match wins.
			if len(r) > matchedLen || (len(r) == matchedLen && roots[i].ReadOnly) {
				matchedLen = len(r)
				canonical = r
			}
		}
	}
	if canonical == "" {
		return "", "", false
	}
	sub, rerr := filepath.Rel(canonical, candidate)
	if rerr != nil || sub == ".." || strings.HasPrefix(sub, ".."+string(filepath.Separator)) {
		return "", "", false
	}
	return canonical, sub, true
}

// jailRoots returns cleaned absolute roots: workspace first, then ExtraRoots and ExtraRootsReadOnly.
// The filesystem root ("/") is never a jail root: granting it disables the
// jail, and the prefix check (`root+sep`) becomes "//" which matches nothing.
//
// Under ReadOnly, the workspace and every unsuffixed ExtraRoot become
// read-only roots; only paths under a WritableRoots entry stay writable. This
// is the explicit writable-root allowlist: a root is writable only when it is
// both a non-read-only jail root and present in WritableRoots while ReadOnly
// holds. Outside ReadOnly, WritableRoots is empty and irrelevant.
func (p *Policy) jailRoots() ([]JailRoot, error) {
	if p == nil {
		return nil, fmt.Errorf("nil policy")
	}
	writableSet := map[string]bool{}
	for _, r := range p.WritableRoots {
		if r == "" {
			continue
		}
		abs, err := filepath.Abs(strings.TrimSpace(r))
		if err != nil {
			continue
		}
		abs = filepath.Clean(abs)
		if resolvedRoot, err := filepath.EvalSymlinks(abs); err == nil {
			abs = filepath.Clean(resolvedRoot)
		}
		writableSet[abs] = true
	}

	var out []JailRoot
	seen := map[string]int{}

	add := func(raw string, label string, ro bool) error {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			return nil
		}
		abs, err := filepath.Abs(raw)
		if err != nil {
			return err
		}
		abs = filepath.Clean(abs)
		if abs == string(filepath.Separator) {
			return fmt.Errorf("%s: filesystem root is not allowed as a path jail root", label)
		}
		// Canonicalize for the writable-allowlist lookup so a symlinked root
		// matches its resolved form; otherwise two link spellings of the same
		// directory would defeat the allowlist.
		canonical := abs
		if resolvedRoot, err := filepath.EvalSymlinks(abs); err == nil {
			canonical = filepath.Clean(resolvedRoot)
		}
		// Under ReadOnly the only writable roots are those in the explicit
		// WritableRoots allowlist; the workspace and unsuffixed extra roots
		// become read-only. Outside ReadOnly WritableRoots is empty/irrelevant.
		if p.ReadOnly && !writableSet[canonical] {
			ro = true
		}
		if idx, ok := seen[abs]; ok {
			if ro {
				out[idx].ReadOnly = true // Read-only restriction wins on duplicate
			}
			return nil
		}
		seen[abs] = len(out)
		out = append(out, JailRoot{Path: abs, ReadOnly: ro})
		return nil
	}

	if err := add(p.Workspace, "workspace", false); err != nil {
		return nil, err
	}
	for _, r := range p.ExtraRoots {
		if err := add(r, "extra_roots", false); err != nil {
			return nil, err
		}
	}
	for _, r := range p.ExtraRootsReadOnly {
		if err := add(r, "extra_roots_read_only", true); err != nil {
			return nil, err
		}
	}
	return out, nil
}

const maxSymlinkDepth = 40

// resolveSymlinks is EvalSymlinks that also handles not-yet-existing paths:
// the nearest existing ancestor is resolved and the remainder re-appended, and
// a dangling symlink component is followed manually.
func resolveSymlinks(path string, depth int) (string, error) {
	if depth > maxSymlinkDepth {
		return "", fmt.Errorf("too many symlinks resolving %q", path)
	}
	if r, err := filepath.EvalSymlinks(path); err == nil {
		return r, nil
	}
	dir := filepath.Dir(path)
	if dir == path { // filesystem root
		return path, nil
	}
	rdir, err := resolveSymlinks(dir, depth+1)
	if err != nil {
		return "", err
	}
	full := filepath.Join(rdir, filepath.Base(path))
	if fi, err := os.Lstat(full); err == nil && fi.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(full)
		if err != nil {
			return "", err
		}
		if !filepath.IsAbs(target) {
			target = filepath.Join(rdir, target)
		}
		return resolveSymlinks(target, depth+1)
	}
	return full, nil
}
