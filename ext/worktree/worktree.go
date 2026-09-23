// Package worktree manages git worktrees as dedicated sibling directories so
// parallel changes each get their own checkout and branch: run a second mow
// (or mowi) session in one while the main checkout keeps working.
//
// Layout: for a repo at /src/app, worktree "fix-auth" lives at
// /src/app-worktrees/fix-auth on branch wt/fix-auth. Keeping them outside the
// repo means the main checkout's tools, globs and git status never see them.
//
// Surfaces: `mow worktree` (CLI), `/worktree` (slash, so hosts such as mowi
// get it over ACP), and worktree_list / worktree_add / worktree_remove tools.
// Blank-import to link.
package worktree

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/subosito/mow/internal/gitx"
)

// BranchPrefix namespaces branches this package creates, so Remove never
// deletes a branch a human made and `git branch` shows which are mow's.
const BranchPrefix = "wt/"

// dirSuffix names the sibling directory that holds a repo's worktrees.
const dirSuffix = "-worktrees"

// maxNameLen bounds a worktree name (it becomes a directory and branch).
const maxNameLen = 64

var validName = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)

// Info is one worktree of the repository.
type Info struct {
	Name   string `json:"name,omitempty"` // set for worktrees this package manages
	Path   string `json:"path"`
	Branch string `json:"branch,omitempty"`
	Head   string `json:"head,omitempty"`
	Main   bool   `json:"main,omitempty"` // the primary checkout
	Dirty  bool   `json:"dirty,omitempty"`
}

// ValidateName checks a worktree name: lowercase letters, digits, '.', '_',
// '-', starting with a letter or digit. The name is used verbatim as both the
// directory and the branch suffix, so nothing is silently rewritten.
func ValidateName(name string) error {
	if name == "" {
		return errors.New("worktree: name required")
	}
	if len(name) > maxNameLen {
		return fmt.Errorf("worktree: name longer than %d characters", maxNameLen)
	}
	if !validName.MatchString(name) || strings.Contains(name, "..") {
		return fmt.Errorf("worktree: invalid name %q (use a-z, 0-9, '.', '_', '-')", name)
	}
	return nil
}

// Root returns the main checkout of the repository containing dir, resolving
// through a linked worktree back to the primary one.
func Root(ctx context.Context, dir string) (string, error) {
	common, err := gitx.Run(ctx, dir, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return "", fmt.Errorf("worktree: %s is not inside a git repository", dir)
	}
	common = filepath.Clean(common)
	if filepath.Base(common) != ".git" {
		return "", fmt.Errorf("worktree: bare repositories are not supported (%s)", common)
	}
	return filepath.Dir(common), nil
}

// BaseDir is the sibling directory that holds root's managed worktrees.
func BaseDir(root string) string {
	root = filepath.Clean(root)
	return filepath.Join(filepath.Dir(root), filepath.Base(root)+dirSuffix)
}

// Path is where worktree name lives for the repository rooted at root.
func Path(root, name string) string {
	return filepath.Join(BaseDir(root), name)
}

// Add creates worktree name on a new branch wt/<name> from base (empty base =
// the current HEAD of dir's repository). It fails if the name, directory or
// branch already exists rather than reusing someone's work.
func Add(ctx context.Context, dir, name, base string) (Info, error) {
	if err := ValidateName(name); err != nil {
		return Info{}, err
	}
	root, err := Root(ctx, dir)
	if err != nil {
		return Info{}, err
	}
	path := Path(root, name)
	if _, err := os.Stat(path); err == nil {
		return Info{}, fmt.Errorf("worktree: %s already exists", path)
	}
	branch := BranchPrefix + name
	if _, err := gitx.Run(ctx, root, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch); err == nil {
		return Info{}, fmt.Errorf("worktree: branch %s already exists (remove it or pick another name)", branch)
	}
	if err := os.MkdirAll(BaseDir(root), 0o755); err != nil {
		return Info{}, err
	}
	args := []string{"worktree", "add", "-b", branch, path}
	if base = strings.TrimSpace(base); base != "" {
		if strings.HasPrefix(base, "-") {
			return Info{}, fmt.Errorf("worktree: invalid base %q", base)
		}
		args = append(args, base)
	}
	if _, err := gitx.Run(ctx, root, args...); err != nil {
		return Info{}, err
	}
	head, _ := gitx.Run(ctx, path, "rev-parse", "--short", "HEAD")
	return Info{Name: name, Path: path, Branch: branch, Head: head}, nil
}

// List returns every worktree of dir's repository, the main checkout first.
// Worktrees under BaseDir on a wt/ branch carry their Name.
func List(ctx context.Context, dir string) ([]Info, error) {
	root, err := Root(ctx, dir)
	if err != nil {
		return nil, err
	}
	out, err := gitx.Run(ctx, root, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, err
	}
	base := BaseDir(root)
	var res []Info
	var cur *Info
	flush := func() {
		if cur == nil {
			return
		}
		if filepath.Clean(cur.Path) == filepath.Clean(root) {
			cur.Main = true
		} else if filepath.Dir(cur.Path) == base && strings.HasPrefix(cur.Branch, BranchPrefix) {
			cur.Name = filepath.Base(cur.Path)
		}
		if st, err := gitx.Run(ctx, cur.Path, "status", "--porcelain"); err == nil {
			cur.Dirty = st != ""
		}
		res = append(res, *cur)
		cur = nil
	}
	for line := range strings.SplitSeq(out, "\n") {
		switch {
		case strings.HasPrefix(line, "worktree "):
			flush()
			cur = &Info{Path: strings.TrimPrefix(line, "worktree ")}
		case cur == nil:
		case strings.HasPrefix(line, "HEAD "):
			if h := strings.TrimPrefix(line, "HEAD "); len(h) >= 7 {
				cur.Head = h[:7]
			}
		case strings.HasPrefix(line, "branch "):
			cur.Branch = strings.TrimPrefix(strings.TrimPrefix(line, "branch "), "refs/heads/")
		}
	}
	flush()
	return res, nil
}

// Remove deletes worktree name and its wt/<name> branch. Without force it
// refuses when the worktree has uncommitted changes, or when the branch has
// commits not merged into the main checkout's HEAD, so work is never lost
// silently.
func Remove(ctx context.Context, dir, name string, force bool) error {
	if err := ValidateName(name); err != nil {
		return err
	}
	root, err := Root(ctx, dir)
	if err != nil {
		return err
	}
	path := Path(root, name)
	branch := BranchPrefix + name
	list, err := List(ctx, root)
	if err != nil {
		return err
	}
	var found *Info
	for i := range list {
		if list[i].Name == name {
			found = &list[i]
			break
		}
	}
	if found == nil {
		return fmt.Errorf("worktree: no managed worktree %q (see `mow worktree list`)", name)
	}
	if !force {
		if found.Dirty {
			return fmt.Errorf("worktree: %s has uncommitted changes (commit them, or remove with --force)", path)
		}
		if ahead, _ := gitx.Run(ctx, root, "rev-list", "--count", "HEAD.."+branch); ahead != "" && ahead != "0" {
			return fmt.Errorf("worktree: branch %s has %s commit(s) not merged into %s (merge it, or remove with --force)", branch, ahead, currentBranchName(ctx, root))
		}
	}
	rmArgs := []string{"worktree", "remove", path}
	if force {
		rmArgs = []string{"worktree", "remove", "--force", path}
	}
	if _, err := gitx.Run(ctx, root, rmArgs...); err != nil {
		return err
	}
	delFlag := "-d"
	if force {
		delFlag = "-D"
	}
	if _, err := gitx.Run(ctx, root, "branch", delFlag, branch); err != nil {
		return fmt.Errorf("worktree removed, but branch %s was kept: %w", branch, err)
	}
	// Drop the sibling directory once it holds nothing else.
	_ = os.Remove(BaseDir(root))
	return nil
}

func currentBranchName(ctx context.Context, root string) string {
	if b, err := gitx.Run(ctx, root, "rev-parse", "--abbrev-ref", "HEAD"); err == nil && b != "" {
		return b
	}
	return "HEAD"
}
