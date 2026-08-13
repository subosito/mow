package review

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// maxScopeFileRead is a hard ceiling on a single file pulled into scope,
// above any --budget override, so a huge blob cannot be ReadAll'd.
const maxScopeFileRead = 8 << 20

// maxExpandCandidates caps filepath.WalkDir results so a huge tree cannot
// allocate an unbounded candidate list before gather applies MaxFiles.
// Default-exclude directories are SkipDir'd unless IncludeAll.
var maxExpandCandidates = 4096

// maxExcludedFiles caps Scope.Excluded so a truncated walk cannot emit an
// unbounded skip list into the report.
const maxExcludedFiles = 256

// pathWithinWorkspace reports whether path is workspace or a descendant after
// cleaning. Symlinks are not followed; pair with rejectSymlinkComponents before
// open so intermediate directory links cannot escape the workspace jail.
func pathWithinWorkspace(workspace, path string) bool {
	workspace = filepath.Clean(workspace)
	path = filepath.Clean(path)
	if workspace == "" || path == "" {
		return false
	}
	if !filepath.IsAbs(workspace) || !filepath.IsAbs(path) {
		return false
	}
	if workspace == path {
		return true
	}
	rel, err := filepath.Rel(workspace, path)
	if err != nil {
		return false
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return false
	}
	return !filepath.IsAbs(rel)
}

// rejectSymlinkComponents Lstats every component from workspace through path and
// rejects symlinks. Without this, a symlink under the workspace could make
// readWorkspaceFile follow out of the review root.
func rejectSymlinkComponents(workspace, path string) error {
	workspace = filepath.Clean(workspace)
	path = filepath.Clean(path)
	if !pathWithinWorkspace(workspace, path) {
		return fmt.Errorf("path escapes the workspace")
	}
	rel, err := filepath.Rel(workspace, path)
	if err != nil {
		return err
	}
	if _, err := os.Lstat(workspace); err != nil {
		return err
	}
	// The workspace root itself may be a symlink (common: home dir → disk).
	// That is the intended tree, not an escape. Only components *under* the
	// root are refused so a planted link cannot redirect the read.
	if rel == "." {
		return nil
	}
	cur := workspace
	parts := strings.Split(rel, string(os.PathSeparator))
	for i, part := range parts {
		if part == "" || part == "." {
			continue
		}
		if part == ".." {
			return fmt.Errorf("path escapes the workspace")
		}
		cur = filepath.Join(cur, part)
		info, err := os.Lstat(cur)
		if err != nil {
			if os.IsNotExist(err) && i == len(parts)-1 {
				return nil
			}
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("symlink path component")
		}
	}
	return nil
}

// readWorkspaceFile reads a workspace-relative path for scope gathering. It
// refuses symlinks and non-regular files so a review cannot be steered at paths
// outside the declared workspace.
func readWorkspaceFile(workspace, rel string) ([]byte, error) {
	full := filepath.Join(workspace, filepath.FromSlash(rel))
	if err := rejectSymlinkComponents(workspace, full); err != nil {
		return nil, fmt.Errorf("review: %w", err)
	}
	f, st, err := openRegular(full)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if !st.Mode().IsRegular() {
		return nil, fmt.Errorf("review: not a regular file")
	}
	if st.Size() > maxScopeFileRead {
		return nil, fmt.Errorf("review: file exceeds %d byte read cap", maxScopeFileRead)
	}
	data, err := io.ReadAll(io.LimitReader(f, maxScopeFileRead+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxScopeFileRead {
		return nil, fmt.Errorf("review: file exceeds %d byte read cap", maxScopeFileRead)
	}
	return data, nil
}
