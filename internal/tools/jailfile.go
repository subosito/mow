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

// openJailed resolves rel under the policy jail, opens that path, then
// verifies the opened fd still lands inside the jail. A symlink swap between
// resolve and open is caught by the post-open check (see fdPath).
func openJailed(p *policy.Policy, rel string, flag int, perm os.FileMode) (*os.File, string, error) {
	if p == nil {
		return nil, "", fmt.Errorf("workspace not set")
	}
	path, err := p.ResolvePath(rel)
	if err != nil {
		return nil, "", err
	}
	if afterResolveHook != nil {
		afterResolveHook(path)
	}
	return OpenJailedPath(p, path, flag, perm)
}

// OpenJailedPath opens an already-resolved absolute path and verifies the fd.
func OpenJailedPath(p *policy.Policy, path string, flag int, perm os.FileMode) (*os.File, string, error) {
	f, err := os.OpenFile(path, flag, perm)
	if err != nil {
		return nil, path, err
	}
	if err := VerifyFDInJail(p, f); err != nil {
		actual, _ := fdPath(f)
		_ = f.Close()
		// If create/trunc landed outside the jail, remove the stray file.
		if actual != "" && flag&(os.O_CREATE|os.O_WRONLY|os.O_RDWR|os.O_TRUNC|os.O_APPEND) != 0 {
			_ = os.Remove(actual)
		}
		return nil, path, err
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
// with post-open verification. On a post-open jail failure after create, the
// outside file is removed.
func WriteFileJailed(p *policy.Policy, rel string, data []byte, perm os.FileMode) (path string, err error) {
	if p == nil {
		return "", fmt.Errorf("workspace not set")
	}
	path, err = p.ResolvePath(rel)
	if err != nil {
		return "", err
	}
	if afterResolveHook != nil {
		afterResolveHook(path)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return path, err
	}
	f, path, err := OpenJailedPath(p, path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm)
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
	if f == nil {
		return fmt.Errorf("path jail: nil file")
	}
	actual, err := fdPath(f)
	if err != nil {
		return fmt.Errorf("path jail: cannot verify open path: %w", err)
	}
	if _, err := p.ResolvePath(actual); err != nil {
		return fmt.Errorf("path %q escapes workspace after open", actual)
	}
	return nil
}

func verifyFDInJail(p *policy.Policy, f *os.File) error {
	return VerifyFDInJail(p, f)
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
