package goal

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// WorkerWorktree marks a plan item to run inside its own git worktree.
// PlanItem.Worker is free-form so unknown values degrade to a normal step
// rather than failing a goal on a typo.
const WorkerWorktree = "worktree"

// worktreeBranchPrefix namespaces branches this pack creates, so cleanup never
// touches a human's branch and a stale branch is recognizable.
const worktreeBranchPrefix = "mow-wt-"

// maxMergeDiffChars bounds the diff summary attached to a step result: it is a
// human/model orientation aid, not the patch itself.
const maxMergeDiffChars = 2000

// gitCommandTimeout bounds one git invocation so a hung remote cannot stall a
// goal step forever (parent ctx cancel still applies).
const gitCommandTimeout = 10 * time.Minute

// isWorktreeItem reports whether an item opted into worktree isolation.
func isWorktreeItem(it PlanItem) bool {
	return strings.EqualFold(strings.TrimSpace(it.Worker), WorkerWorktree)
}

// git runs one git command in dir and returns trimmed combined output.
func git(ctx context.Context, dir string, args ...string) (string, error) {
	tctx, cancel := context.WithTimeout(ctx, gitCommandTimeout)
	defer cancel()
	cmd := exec.CommandContext(tctx, "git", args...)
	cmd.Dir = dir
	setGitProcAttr(cmd)
	out, err := cmd.CombinedOutput()
	text := strings.TrimSpace(string(out))
	if err != nil {
		msg := redactSecrets(truncateRunes(text, 400))
		return msg, fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, msg)
	}
	return text, nil
}

// isGitRepo reports whether dir is inside a git work tree. Any failure (git
// missing, not a repo, bare repo) is a plain false: worktree isolation is an
// optimization, never a reason to fail a goal.
func isGitRepo(ctx context.Context, dir string) bool {
	if strings.TrimSpace(dir) == "" {
		return false
	}
	out, err := git(ctx, dir, "rev-parse", "--is-inside-work-tree")
	return err == nil && strings.TrimSpace(out) == "true"
}

// currentBranch returns the branch to merge back into. A detached HEAD has no
// branch to merge into, so worktree isolation is declined.
func currentBranch(ctx context.Context, dir string) (string, error) {
	out, err := git(ctx, dir, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return "", err
	}
	branch := strings.TrimSpace(out)
	if branch == "" || branch == "HEAD" {
		return "", fmt.Errorf("detached HEAD")
	}
	return branch, nil
}

// worktree is one isolated checkout for a single plan item.
type worktree struct {
	Dir    string
	Branch string
	Base   string
	Parent string
}

// addWorktree creates a branch + worktree for item under the parent repo.
func addWorktree(ctx context.Context, parent, goalID, itemID string) (*worktree, error) {
	base, err := currentBranch(ctx, parent)
	if err != nil {
		return nil, err
	}
	repoMu.Lock()
	defer repoMu.Unlock()

	branch := worktreeBranchPrefix + slugPathPart(goalID) + "-" + slugPathPart(itemID)
	dir, err := os.MkdirTemp("", "mow-wt-")
	if err != nil {
		return nil, err
	}
	// MkdirTemp created the path; git worktree add requires it absent.
	path := filepath.Join(dir, "wt")
	if _, err := git(ctx, parent, "worktree", "add", path, "-b", branch); err != nil {
		os.RemoveAll(dir)
		return nil, err
	}
	return &worktree{Dir: path, Branch: branch, Base: base, Parent: parent}, nil
}

// commitAll stages and commits everything in the worktree. Reports whether a
// commit was created: a step that changed nothing is success with no commit.
func (w *worktree) commitAll(ctx context.Context, message string) (bool, error) {
	if _, err := git(ctx, w.Dir, "add", "-A"); err != nil {
		return false, err
	}
	// --quiet --exit-code: 0 means no staged changes.
	if _, err := git(ctx, w.Dir, "diff", "--cached", "--quiet"); err == nil {
		return false, nil
	}
	// Identity may be unset in CI/sandboxes; -c keeps it local to this commit
	// and never writes to the user's git config.
	args := []string{
		"-c", "user.name=mow",
		"-c", "user.email=mow@localhost",
		"commit", "-m", message,
	}
	if _, err := git(ctx, w.Dir, args...); err != nil {
		return false, err
	}
	return true, nil
}

// mergeResult describes the outcome of merging a worker branch back.
type mergeResult struct {
	Merged     bool
	Conflicted bool
	Diff       string
	Err        error
}

// repoMu serializes operations that mutate the PARENT repository: worktree
// add/remove and merges. Worktrees isolate the *work*, but these operations
// all touch the parent's index, HEAD or .git/worktrees, and git takes an
// exclusive index.lock for them. Concurrent workers therefore queue at the
// join point — this is the "manager merges" half of the branch-and-merge
// pattern, not an accidental bottleneck. Work inside a worktree (the part
// that actually takes time) stays fully parallel.
var repoMu sync.Mutex

// merge brings the worker branch back into the base branch with --no-ff, so
// every worker's work is a visible, revertable merge commit.
//
// A conflict is never resolved here: the merge is aborted so the parent
// workspace is left clean, and the caller escalates to a human.
func (w *worktree) merge(ctx context.Context) mergeResult {
	repoMu.Lock()
	defer repoMu.Unlock()

	// Diff before merging: after a conflict abort there is nothing to show.
	diff, _ := git(ctx, w.Parent, "diff", "--stat", w.Base+".."+w.Branch)

	if _, err := git(ctx, w.Parent, "checkout", w.Base); err != nil {
		return mergeResult{Err: err, Diff: diff}
	}
	if _, err := git(ctx, w.Parent, "merge", "--no-ff", "-m",
		"mow: merge "+w.Branch, w.Branch); err != nil {
		// Distinguish a real conflict from an operational failure: only a
		// conflict leaves MERGE_HEAD behind.
		if _, statErr := os.Stat(filepath.Join(w.gitDir(ctx), "MERGE_HEAD")); statErr == nil {
			_, _ = git(ctx, w.Parent, "merge", "--abort")
			return mergeResult{Conflicted: true, Diff: diff}
		}
		return mergeResult{Err: err, Diff: diff}
	}
	return mergeResult{Merged: true, Diff: diff}
}

// gitDir resolves the parent repo's .git directory (worktrees make it indirect).
func (w *worktree) gitDir(ctx context.Context) string {
	out, err := git(ctx, w.Parent, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return filepath.Join(w.Parent, ".git")
	}
	return strings.TrimSpace(out)
}

// WorktreeInfo is one mow-created worktree, for hosts that surface a
// "leftover conflicted worktrees" list (see ListWorktrees).
type WorktreeInfo struct {
	Dir    string `json:"dir"`
	Branch string `json:"branch"`
	GoalID string `json:"goal_id,omitempty"`
	ItemID string `json:"item_id,omitempty"`
}

// ListWorktrees returns mow-created worktrees still on disk under dir
// (branches with the mow-wt- prefix, e.g. leftover merge conflicts kept for
// human inspection). Empty when dir is not a git repo or nothing remains.
func ListWorktrees(ctx context.Context, dir string) ([]WorktreeInfo, error) {
	out, err := git(ctx, dir, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, err // not a git repo or git missing
	}
	var res []WorktreeInfo
	cur := WorktreeInfo{}
	for _, line := range strings.Split(out, "\n") {
		switch {
		case strings.HasPrefix(line, "worktree "):
			cur = WorktreeInfo{Dir: strings.TrimSpace(strings.TrimPrefix(line, "worktree "))}
		case strings.HasPrefix(line, "branch "):
			b := strings.TrimPrefix(line, "branch ")
			b = strings.TrimPrefix(b, "refs/heads/")
			if strings.HasPrefix(b, worktreeBranchPrefix) {
				cur.Branch = b
				rest := strings.TrimPrefix(b, worktreeBranchPrefix)
				if i := strings.LastIndexByte(rest, '-'); i > 0 {
					cur.GoalID, cur.ItemID = rest[:i], rest[i+1:]
				}
				res = append(res, cur)
			}
		}
	}
	return res, nil
}

// cleanup removes the worktree and its branch. Callers keep conflicted
// worktrees on disk for human inspection instead of calling this.
func (w *worktree) cleanup(ctx context.Context) {
	repoMu.Lock()
	defer repoMu.Unlock()

	_, _ = git(ctx, w.Parent, "worktree", "remove", "--force", w.Dir)
	_, _ = git(ctx, w.Parent, "branch", "-D", w.Branch)
	os.RemoveAll(filepath.Dir(w.Dir))
}

// slugPathPart makes an id safe for a branch name.
func slugPathPart(s string) string {
	s = strings.TrimSpace(strings.ToLower(s))
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-' || r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		out = "item"
	}
	return truncateRunes(out, 40)
}
