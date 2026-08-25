package tools

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/subosito/mow/internal/policy"
)

// afterResolveHook runs after ResolvePath succeeds and before open/create.
// Tests use it to simulate a symlink race (TOCTOU). Nil in production.
var afterResolveHook func(resolved string)

// openJailed resolves rel under the policy jail, then opens it. On Linux
// kernels with openat2 the open is descriptor-relative with RESOLVE_BENEATH:
// the KERNEL refuses any component escaping the matched root (symlink or ..)
// during resolution — no TOCTOU window between the check and the open.
// Without openat2, the legacy open + post-open fdPath verification applies;
// either way a symlink swap between resolve and open cannot leak or write
// outside the jail.
func openJailed(p *policy.Policy, rel string, flag int, perm os.FileMode) (*os.File, string, error) {
	if p == nil {
		return nil, "", fmt.Errorf("workspace not set")
	}
	write := flag&(os.O_WRONLY|os.O_RDWR|os.O_CREATE|os.O_TRUNC|os.O_APPEND) != 0
	path, err := p.ResolvePathFor(rel, write)
	if err != nil {
		return nil, "", err
	}
	if afterResolveHook != nil {
		afterResolveHook(path)
	}
	// Kernel-enforced fast path: open beneath the matched root (same matcher
	// as the policy check above, canonical directory). Defenses in depth:
	// the root is opened O_DIRECTORY|O_NOFOLLOW (a swapped root symlink must
	// not redirect confinement), and the resulting fd is still verified
	// against the jail by name.
	if root, sub, ok := p.Beneath(path); ok {
		if rootF, rerr := os.OpenFile(root, rootOpenFlags, 0); rerr == nil {
			f, supported, oerr := openatBeneath(rootF.Fd(), sub, flag, perm)
			_ = rootF.Close()
			if supported {
				if oerr != nil {
					return nil, path, oerr
				}
				if verr := VerifyFDInJailFor(p, f, write); verr != nil {
					_ = f.Close()
					return nil, path, verr
				}
				return f, path, nil
			}
		}
	}
	return OpenJailedPathFor(p, path, flag, perm, write)
}

// OpenJailedPathFor opens an already-resolved absolute path and verifies the
// fd with write context.
//
// Destructive flags are sequenced AFTER verification: opening with O_TRUNC
// would already have destroyed an outside file (raced symlink swap) by the
// time the post-open check rejects it. Instead we open read-write (no trunc),
// verify the descriptor, then truncate — so the window between resolve and
// verify never truncates, and a failed verification leaves the outside file
// intact. On verification failure we only ever remove a stray O_CREATE may
// have introduced: with other flags the fd may reference a pre-existing
// outside inode, and deleting by pathname would destroy it.
func OpenJailedPathFor(p *policy.Policy, path string, flag int, perm os.FileMode, write bool) (*os.File, string, error) {
	// Strip destructive bits for the initial open; replay them post-verify.
	// Truncate needs write access, so swap the access mode to O_RDWR.
	trunc := flag&os.O_TRUNC != 0
	openFlag := flag &^ os.O_TRUNC
	if trunc {
		openFlag &^= os.O_WRONLY | os.O_RDWR // clear access mode bits
		openFlag |= os.O_RDWR
	}
	f, err := os.OpenFile(path, openFlag, perm)
	if err != nil {
		return nil, path, err
	}
	if err := VerifyFDInJailFor(p, f, write); err != nil {
		actual, _ := fdPath(f)
		_ = f.Close()
		if actual != "" && flag&os.O_CREATE != 0 {
			_ = os.Remove(actual)
		}
		return nil, path, err
	}
	if trunc {
		if err := f.Truncate(0); err != nil {
			_ = f.Close()
			return nil, path, err
		}
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			_ = f.Close()
			return nil, path, err
		}
	}
	return f, path, nil
}

// ReadFileJailed reads a file under the path jail with post-open verification.
func ReadFileJailed(p *policy.Policy, rel string) (path string, data []byte, err error) {
	f, path, err := openJailed(p, rel, os.O_RDONLY, 0)
	if err != nil {
		return path, nil, err
	}
	defer f.Close()
	data, err = io.ReadAll(f)
	return path, data, err
}

// readFileJailed keeps the internal built-in tool call sites concise.
func readFileJailed(p *policy.Policy, rel string) (string, []byte, error) {
	return ReadFileJailed(p, rel)
}

// WriteFileJailed creates parent dirs, then writes data under the path jail
// (kernel-enforced open on Linux; post-open verification otherwise).
func WriteFileJailed(p *policy.Policy, rel string, data []byte, perm os.FileMode) (path string, err error) {
	if p == nil {
		return "", fmt.Errorf("workspace not set")
	}
	// Resolve to know the resolved path for MkdirAll; openJailed re-checks
	// and re-resolves, keeping jail membership the authority.
	path, err = p.ResolvePathFor(rel, true)
	if err != nil {
		return path, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return path, err
	}
	f, path, err := openJailed(p, path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm)
	if err != nil {
		return path, err
	}
	_, werr := f.Write(data)
	cerr := f.Close()
	if werr != nil {
		return path, werr
	}
	return path, cerr
}

func writeFileJailed(p *policy.Policy, rel string, data []byte, perm os.FileMode) (string, error) {
	return WriteFileJailed(p, rel, data, perm)
}

// VerifyFDInJail ensures the file the kernel opened is still under the jail.
// Uses the platform fd→path mapping (see jailfile_*.go); fails closed if the
// path cannot be determined.
func VerifyFDInJail(p *policy.Policy, f *os.File) error {
	return VerifyFDInJailFor(p, f, false)
}

// VerifyFDInJailFor verifies the opened fd stays inside the path jail for
// read or write. Uses the platform fd→path mapping (see jailfile_*.go);
// fails closed if the path cannot be determined.
func VerifyFDInJailFor(p *policy.Policy, f *os.File, write bool) error {
	if f == nil {
		return fmt.Errorf("path jail: nil file")
	}
	actual, err := fdPath(f)
	if err != nil {
		return fmt.Errorf("path jail: cannot verify open path: %w", err)
	}
	if _, err := p.ResolvePathFor(actual, write); err != nil {
		return fmt.Errorf("path %q escapes workspace after open: %w", actual, err)
	}
	return nil
}

// workspaceRel renders a resolved path relative to the workspace for display:
// in-workspace files show a clean relative path ("internal/tools/x.go"),
// extra-root files show "../mowi/…". Falls back to the raw path when Rel
// cannot produce a useful result.
func workspaceRel(ws, abs string) string {
	if ws == "" || abs == "" {
		return abs
	}
	if rel, err := filepath.Rel(ws, abs); err == nil {
		return rel
	}
	return abs
}
