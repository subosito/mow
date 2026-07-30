package review

import (
	"context"
	"errors"
	"os/exec"
	"strconv"
	"strings"
)

// gitRunner executes a git command in the workspace. Swapped in tests.
type gitRunner func(ctx context.Context, workspace string, args ...string) (string, error)

// runGit executes git with a workspace cwd and returns trimmed stdout.
func runGit(ctx context.Context, workspace string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = workspace
	// Keep git non-interactive: a credential or editor prompt in CI would hang.
	cmd.Env = append(cmd.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0")
	out, err := cmd.Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && len(ee.Stderr) > 0 {
			return "", &GitError{Args: args, Err: err, Stderr: strings.TrimSpace(string(ee.Stderr))}
		}
		return "", &GitError{Args: args, Err: err}
	}
	return strings.TrimRight(string(out), "\n"), nil
}

// GitError carries the failing git invocation and its stderr for diagnostics.
type GitError struct {
	Args   []string
	Err    error
	Stderr string
}

func (e *GitError) Error() string {
	msg := "git " + strings.Join(e.Args, " ") + ": " + e.Err.Error()
	if e.Stderr != "" {
		msg += ": " + e.Stderr
	}
	return msg
}

func (e *GitError) Unwrap() error { return e.Err }

// GitContext is repository metadata attached to a report for traceability.
type GitContext struct {
	Available bool   `json:"available"`
	Commit    string `json:"commit,omitempty"`
	Branch    string `json:"branch,omitempty"`
	Dirty     bool   `json:"dirty,omitempty"`
}

// gitContext collects HEAD/branch/dirty state; a non-repo workspace is not an
// error, it just yields Available=false.
func gitContext(ctx context.Context, workspace string, git gitRunner) GitContext {
	if _, err := git(ctx, workspace, "rev-parse", "--is-inside-work-tree"); err != nil {
		return GitContext{}
	}
	gc := GitContext{Available: true}
	if out, err := git(ctx, workspace, "rev-parse", "--short", "HEAD"); err == nil {
		gc.Commit = strings.TrimSpace(out)
	}
	if out, err := git(ctx, workspace, "rev-parse", "--abbrev-ref", "HEAD"); err == nil {
		gc.Branch = strings.TrimSpace(out)
	}
	if out, err := git(ctx, workspace, "status", "--porcelain"); err == nil {
		gc.Dirty = strings.TrimSpace(out) != ""
	}
	return gc
}

// changedFiles lists files touched by a git selector, filtering deletions
// (a deleted file has nothing left to review).
func changedFiles(ctx context.Context, workspace string, git gitRunner, args ...string) ([]string, error) {
	// --diff-filter excludes deletions; -z would complicate the trivial split.
	full := append([]string{"diff", "--name-only", "--diff-filter=ACMRT"}, args...)
	out, err := git(ctx, workspace, full...)
	if err != nil {
		return nil, err
	}
	return splitLines(out), nil
}

// diffText returns a unified diff with limited context for the given selector.
func diffText(ctx context.Context, workspace string, git gitRunner, contextLines int, args ...string) (string, error) {
	full := []string{"diff", "--no-color", "--diff-filter=ACMRT"}
	if contextLines >= 0 {
		full = append(full, "-U"+strconv.Itoa(contextLines))
	}
	full = append(full, args...)
	return git(ctx, workspace, full...)
}

// splitLines splits git output into non-empty trimmed lines.
func splitLines(s string) []string {
	var out []string
	for _, ln := range strings.Split(s, "\n") {
		if ln = strings.TrimSpace(ln); ln != "" {
			out = append(out, ln)
		}
	}
	return out
}
