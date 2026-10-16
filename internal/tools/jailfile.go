package tools

import (
	"fmt"
	"io"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/subosito/mow/internal/policy"
)

// afterResolveHook runs after ResolvePath succeeds and before open/create.
// Tests use it to simulate a symlink race (TOCTOU). Nil in production.
var afterResolveHook func(resolved string)

// afterTempWriteHook lets tests replace directory entries before commit.
var afterTempWriteHook func(path string)

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
	// O_NONBLOCK: opening a FIFO for read would otherwise block until a writer
	// appears — before requireRegular below can reject it. Regular files
	// ignore the flag.
	flag |= syscall.O_NONBLOCK
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
				if rerr := requireRegular(f, path); rerr != nil {
					_ = f.Close()
					return nil, path, rerr
				}
				return f, path, nil
			}
		}
	}
	f, path, err := OpenJailedPathFor(p, path, flag, perm, write)
	if err != nil {
		return nil, path, err
	}
	if rerr := requireRegular(f, path); rerr != nil {
		_ = f.Close()
		return nil, path, rerr
	}
	return f, path, nil
}

// requireRegular rejects anything but a regular file (FIFO, socket, device,
// directory): the file tools move byte content, and a FIFO would park them
// forever.
func requireRegular(f *os.File, path string) error {
	fi, err := f.Stat()
	if err != nil {
		return err
	}
	if !fi.Mode().IsRegular() {
		return fmt.Errorf("%s: not a regular file (mode %v)", path, fi.Mode().Type())
	}
	return nil
}

// OpenJailedPathFor opens an already-resolved absolute path and verifies the
// fd with write context.
//
// Destructive flags are sequenced AFTER verification: opening with O_TRUNC
// would already have destroyed an outside file (raced symlink swap) by the
// time the post-open check rejects it. Instead we open read-write (no trunc),
// verify the descriptor, then truncate — so the window between resolve and
// verify never truncates, and a failed verification leaves the outside file
// intact. On verification failure we only ever remove a stray file that
// O_CREATE|O_EXCL provably created: with any other flags (O_CREATE alone
// opens an existing file) the fd may reference a pre-existing outside inode,
// and deleting by pathname would destroy it.
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
		if actual != "" && flag&(os.O_CREATE|os.O_EXCL) == os.O_CREATE|os.O_EXCL {
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

// maxEditBytes bounds what the write (for its diff) and edit tools load into
// memory. Above it, write skips the diff and edit refuses the file.
const maxEditBytes = 8 << 20

// readFileJailedCapped is readFileJailed for files up to max bytes; a larger
// file is reported via tooBig without being read.
func readFileJailedCapped(p *policy.Policy, rel string, max int64) (path string, data []byte, tooBig bool, err error) {
	f, path, err := openJailed(p, rel, os.O_RDONLY, 0)
	if err != nil {
		return path, nil, false, err
	}
	defer f.Close()
	if fi, serr := f.Stat(); serr != nil {
		return path, nil, false, serr
	} else if fi.Size() > max {
		return path, nil, true, nil
	}
	data, err = io.ReadAll(io.LimitReader(f, max+1))
	if err != nil {
		return path, nil, false, err
	}
	if int64(len(data)) > max {
		return path, nil, true, nil
	}
	return path, data, false, nil
}

// WriteFileJailed creates parent dirs, then writes data under the path jail
// (kernel-enforced open on Linux; post-open verification otherwise).
//
// The write is atomic where it can be: data lands in a sibling temp file
// (opened relative to a verified parent handle), is fsynced, then renamed over the target.
// A truncate-then-write in place leaves the file empty when the write fails
// halfway — ENOSPC, or the process being killed — destroying the previous
// content. Rename is same-directory, so it cannot fail with EXDEV. Targets we
// cannot replace this way (an existing non-regular file or a symlink, which
// rename would replace rather than write through) keep the in-place path.
func WriteFileJailed(p *policy.Policy, rel string, data []byte, perm os.FileMode) (path string, err error) {
	if p == nil {
		return "", fmt.Errorf("workspace not set")
	}
	// Resolve before making parents; both write paths verify their opened
	// handles against the policy before writing.
	path, err = p.ResolvePathFor(rel, true)
	if err != nil {
		return path, err
	}
	if err := mkdirAllJailed(p, filepath.Dir(path)); err != nil {
		return path, err
	}
	if done, werr := writeViaTemp(p, path, data, perm); done {
		return path, werr
	}
	return writeInPlaceJailed(p, path, data, perm)
}

// writeViaTemp writes data to a sibling temp file and renames it over path.
// done is false when the target needs an in-place write or the temp cannot
// be created. Once a temp exists, failures never fall back to truncation.
func writeViaTemp(p *policy.Policy, path string, data []byte, mode os.FileMode) (done bool, err error) {
	dir, base := filepath.Split(path)
	if afterResolveHook != nil {
		afterResolveHook(path)
	}
	// Pin the parent before creating anything. All later names are single
	// components relative to this handle, even if the parent path is replaced.
	parent, err := os.OpenRoot(dir)
	if err != nil {
		return true, err
	}
	defer parent.Close()
	dirFile, err := parent.Open(".")
	if err != nil {
		return true, err
	}
	err = VerifyFDInJailFor(p, dirFile, true)
	_ = dirFile.Close()
	if err != nil {
		return true, err
	}
	info, err := parent.Lstat(base)
	if err != nil && !os.IsNotExist(err) {
		return true, err
	}
	if info != nil {
		if !info.Mode().IsRegular() {
			return false, nil
		}
		mode = info.Mode().Perm()
	}
	var f *os.File
	var tmp string
	for range 8 {
		cand := "." + base + ".tmp-" + strconv.FormatUint(rand.Uint64(), 36)
		tf, oerr := parent.OpenFile(cand, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
		if oerr != nil {
			if os.IsExist(oerr) {
				continue
			}
			return false, nil
		}
		f, tmp = tf, cand
		break
	}
	if f == nil {
		return false, nil
	}
	defer func() {
		_ = f.Close()
		if err != nil {
			_ = parent.Remove(tmp)
		}
	}()
	if _, werr := f.Write(data); werr != nil {
		return true, werr
	}
	if afterTempWriteHook != nil {
		afterTempWriteHook(filepath.Join(dir, tmp))
	}
	// Only an existing target has an exact mode to preserve. New files keep
	// the umask applied at creation. Use the fd so a replaced name cannot
	// redirect chmod to another file.
	if info != nil {
		if cerr := f.Chmod(mode); cerr != nil {
			return true, cerr
		}
	}
	if serr := f.Sync(); serr != nil {
		return true, serr
	}
	if cerr := f.Close(); cerr != nil {
		return true, cerr
	}
	return true, parent.Rename(tmp, base)
}

func writeInPlaceJailed(p *policy.Policy, path string, data []byte, perm os.FileMode) (string, error) {
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

// mkdirAllJailed creates missing parents of dir, one component at a time,
// refusing symlink components that escape the jail. Unlike os.MkdirAll it
// never follows an escaping link to create directories outside the workspace.
func mkdirAllJailed(p *policy.Policy, dir string) error {
	if p == nil {
		return fmt.Errorf("workspace not set")
	}
	dir = filepath.Clean(dir)
	root, rel, ok := p.Beneath(dir)
	if !ok {
		if _, err := p.ResolvePathFor(dir, true); err != nil {
			return err
		}
		root, rel, ok = p.Beneath(dir)
		if !ok {
			return fmt.Errorf("path %q escapes workspace (and extra roots)", dir)
		}
	}
	if rel == "" || rel == "." {
		return nil
	}
	cur := root
	for part := range strings.SplitSeq(rel, string(filepath.Separator)) {
		if part == "" || part == "." {
			continue
		}
		next := filepath.Join(cur, part)
		fi, err := os.Lstat(next)
		if err == nil {
			if fi.Mode()&os.ModeSymlink != 0 {
				resolved, rerr := filepath.EvalSymlinks(next)
				if rerr != nil {
					return rerr
				}
				if _, err := p.ResolvePathFor(resolved, true); err != nil {
					return err
				}
				cur = resolved
				continue
			}
			if !fi.IsDir() {
				return fmt.Errorf("mkdir: %s is not a directory", next)
			}
			cur = next
			continue
		}
		if !os.IsNotExist(err) {
			return err
		}
		if err := os.Mkdir(next, 0o755); err != nil && !os.IsExist(err) {
			return err
		}
		if _, err := p.ResolvePathFor(next, true); err != nil {
			_ = os.Remove(next)
			return err
		}
		cur = next
	}
	return nil
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
// extra-root files show "../sibling/…". Falls back to the raw path when Rel
// cannot produce a useful result. The workspace is resolved first: abs comes
// from the jail and is symlink-free, so a workspace reached through a symlink
// (macOS /var → /private/var) would otherwise render as "../../../private/…".
func workspaceRel(ws, abs string) string {
	if ws == "" || abs == "" {
		return abs
	}
	if r, err := filepath.EvalSymlinks(ws); err == nil {
		ws = r
	}
	if rel, err := filepath.Rel(ws, abs); err == nil {
		return rel
	}
	return abs
}
