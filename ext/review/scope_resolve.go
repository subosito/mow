package review

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// ResolveScope decides what will be reviewed, before any model call. Keeping
// this deterministic (rather than letting the agent wander) is what makes a
// review reproducible and cheap.
//
// Selection precedence: --diff, then --staged, then --base, then explicit
// paths, then (when the workspace is a dirty git repo) the working tree, and
// finally the whole workspace tree.
func ResolveScope(ctx context.Context, req ScopeRequest) (*Scope, error) {
	return resolveScope(ctx, req, runGit, os.ReadFile)
}

// readFileFunc reads a file by absolute path (swapped in tests).
type readFileFunc func(name string) ([]byte, error)

func resolveScope(ctx context.Context, req ScopeRequest, git gitRunner, readFile readFileFunc) (*Scope, error) {
	ws := strings.TrimSpace(req.Workspace)
	if ws == "" {
		return nil, fmt.Errorf("review: workspace is required")
	}
	abs, err := filepath.Abs(ws)
	if err != nil {
		return nil, fmt.Errorf("review: workspace: %w", err)
	}
	budget, ok := LookupBudget(req.Budget)
	if !ok {
		return nil, fmt.Errorf("review: unknown budget %q (want %s)", req.Budget, strings.Join(BudgetNames(), ", "))
	}

	sc := &Scope{Workspace: abs, Budget: budget, index: map[string]int{}}
	sc.Git = gitContext(ctx, abs, git)

	candidates, err := selectCandidates(ctx, sc, req, git)
	if err != nil {
		return nil, err
	}
	excludes := req.Excludes
	if !req.IncludeAll {
		excludes = append(append([]string(nil), DefaultExcludes()...), req.Excludes...)
	}
	gather(sc, req, candidates, excludes, readFile, git, ctx)
	return sc, nil
}

// selectCandidates produces the ordered candidate path list for the scope.
func selectCandidates(ctx context.Context, sc *Scope, req ScopeRequest, git gitRunner) ([]string, error) {
	switch {
	case strings.TrimSpace(req.Diff) != "":
		sc.Mode, sc.Selector = "diff", strings.TrimSpace(req.Diff)
		if !sc.Git.Available {
			return nil, fmt.Errorf("review: --diff needs a git repository (%s is not one)", sc.Workspace)
		}
		return changedFiles(ctx, sc.Workspace, git, sc.Selector)

	case req.Staged:
		sc.Mode, sc.Selector = "staged", "staged changes"
		if !sc.Git.Available {
			return nil, fmt.Errorf("review: --staged needs a git repository (%s is not one)", sc.Workspace)
		}
		return changedFiles(ctx, sc.Workspace, git, "--cached")

	case strings.TrimSpace(req.Base) != "":
		sc.Mode, sc.Selector = "base", strings.TrimSpace(req.Base)+"...HEAD"
		if !sc.Git.Available {
			return nil, fmt.Errorf("review: --base needs a git repository (%s is not one)", sc.Workspace)
		}
		return changedFiles(ctx, sc.Workspace, git, sc.Selector)

	case len(req.Paths) > 0:
		sc.Mode = "paths"
		sc.Selector = strings.Join(req.Paths, " ")
		return expandPaths(sc.Workspace, req.Paths)

	case sc.Git.Available && sc.Git.Dirty:
		// No selector on a dirty repo: reviewing uncommitted work is the
		// overwhelmingly common intent, and it keeps the default run cheap.
		sc.Mode, sc.Selector = "worktree", "uncommitted changes"
		files, err := changedFiles(ctx, sc.Workspace, git, "HEAD")
		if err != nil {
			return nil, err
		}
		untracked, err := git(ctx, sc.Workspace, "ls-files", "--others", "--exclude-standard")
		if err == nil {
			files = append(files, splitLines(untracked)...)
		}
		return files, nil

	default:
		sc.Mode, sc.Selector = "paths", "."
		return expandPaths(sc.Workspace, []string{"."})
	}
}

// expandPaths walks explicit files/directories into a sorted file list.
func expandPaths(workspace string, paths []string) ([]string, error) {
	var out []string
	seen := map[string]bool{}
	for _, p := range paths {
		rel, err := NormalizePath(p, workspace)
		if err != nil {
			// "." means the whole workspace.
			if strings.TrimSpace(p) == "." || strings.TrimSpace(p) == "./" {
				rel = "."
			} else {
				return nil, fmt.Errorf("review: %w", err)
			}
		}
		full := filepath.Join(workspace, filepath.FromSlash(rel))
		info, err := os.Stat(full)
		if err != nil {
			return nil, fmt.Errorf("review: %s: %w", rel, err)
		}
		if !info.IsDir() {
			if !seen[rel] {
				seen[rel] = true
				out = append(out, rel)
			}
			continue
		}
		err = filepath.WalkDir(full, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil // unreadable entries are skipped, not fatal
			}
			if d.IsDir() {
				// Only .git is pruned here: it is repository metadata, never
				// reviewable source. vendor/node_modules deliberately fall
				// through to the exclusion pass in gather(), so they are
				// reported with a reason and --include-all can override them.
				// Pruning them here made those skips invisible and permanent.
				if d.Name() == ".git" {
					return filepath.SkipDir
				}
				return nil
			}
			r, err := filepath.Rel(workspace, p)
			if err != nil {
				return nil
			}
			r = filepath.ToSlash(r)
			if !seen[r] {
				seen[r] = true
				out = append(out, r)
			}
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("review: walk %s: %w", rel, err)
		}
	}
	sort.Strings(out)
	return out, nil
}

// gather applies excludes and budgets, then loads content/diffs for the files
// that make the cut. Everything dropped is recorded with a reason.
func gather(sc *Scope, req ScopeRequest, candidates, excludes []string, readFile readFileFunc, git gitRunner, ctx context.Context) {
	diffMode := sc.Mode == "diff" || sc.Mode == "staged" || sc.Mode == "base" || sc.Mode == "worktree"
	ctxLines := req.DiffContextLines
	if ctxLines <= 0 {
		ctxLines = 6
	}
	for _, rel := range candidates {
		if pat, hit := MatchAny(excludes, rel); hit {
			sc.Excluded = append(sc.Excluded, ExcludedFile{Path: rel, Reason: "excluded by " + pat})
			continue
		}
		if len(sc.Files) >= sc.Budget.MaxFiles {
			sc.Excluded = append(sc.Excluded, ExcludedFile{Path: rel, Reason: "over budget (max files)"})
			sc.Truncated = true
			sc.TruncReason = fmt.Sprintf("budget %s: file limit %d reached", sc.Budget.Name, sc.Budget.MaxFiles)
			continue
		}
		full := filepath.Join(sc.Workspace, filepath.FromSlash(rel))
		raw, err := readFile(full)
		if err != nil {
			sc.Excluded = append(sc.Excluded, ExcludedFile{Path: rel, Reason: "unreadable: " + err.Error()})
			continue
		}
		if len(raw) > sc.Budget.MaxFileBytes {
			sc.Excluded = append(sc.Excluded, ExcludedFile{Path: rel, Reason: fmt.Sprintf("file too large (%d bytes)", len(raw))})
			continue
		}
		if isBinary(raw) {
			sc.Excluded = append(sc.Excluded, ExcludedFile{Path: rel, Reason: "binary file"})
			continue
		}
		text := string(raw)
		if !req.IncludeAll && LooksGenerated(head(text)) {
			sc.Excluded = append(sc.Excluded, ExcludedFile{Path: rel, Reason: "generated file"})
			continue
		}

		f := ScopeFile{Path: rel, Bytes: len(raw), Lines: countLines(text)}
		if diffMode {
			f.Diff = fileDiff(ctx, sc, git, ctxLines, rel)
		}
		// Full content is included for path reviews, for files with no diff
		// (new/untracked files have nothing to diff against), and for small
		// files where the extra context is worth more than the saved bytes.
		if !diffMode || f.Diff == "" || len(raw) <= sc.Budget.MaxFileBytes/4 {
			f.Content = text
		}
		cost := len(f.Diff) + len(f.Content)
		if sc.TotalBytes+cost > sc.Budget.MaxBytes {
			// Try diff-only before giving up on the file entirely.
			if f.Diff != "" && sc.TotalBytes+len(f.Diff) <= sc.Budget.MaxBytes {
				f.Content = ""
				cost = len(f.Diff)
			} else {
				sc.Excluded = append(sc.Excluded, ExcludedFile{Path: rel, Reason: "over budget (max bytes)"})
				sc.Truncated = true
				sc.TruncReason = fmt.Sprintf("budget %s: byte limit %d reached", sc.Budget.Name, sc.Budget.MaxBytes)
				continue
			}
		}
		if f.Diff == "" && f.Content == "" {
			// Nothing to show the model: don't spend a scope slot on it.
			sc.Excluded = append(sc.Excluded, ExcludedFile{Path: rel, Reason: "no diff or content available"})
			continue
		}
		sc.TotalBytes += cost
		sc.index[rel] = len(sc.Files)
		sc.Files = append(sc.Files, f)
	}
}

// fileDiff fetches the per-file diff for the current scope mode.
func fileDiff(ctx context.Context, sc *Scope, git gitRunner, ctxLines int, rel string) string {
	var args []string
	switch sc.Mode {
	case "staged":
		args = []string{"--cached", "--", rel}
	case "worktree":
		args = []string{"HEAD", "--", rel}
	default:
		args = []string{sc.Selector, "--", rel}
	}
	out, err := diffText(ctx, sc.Workspace, git, ctxLines, args...)
	if err != nil {
		return ""
	}
	return out
}

// isBinary uses the NUL-byte heuristic over the first 8KiB.
func isBinary(b []byte) bool {
	if len(b) > 8192 {
		b = b[:8192]
	}
	for _, c := range b {
		if c == 0 {
			return true
		}
	}
	return false
}

// head returns the first ~1KiB for generated-file detection.
func head(s string) string {
	if len(s) > 1024 {
		return s[:1024]
	}
	return s
}

func countLines(s string) int {
	if s == "" {
		return 0
	}
	n := strings.Count(s, "\n")
	if !strings.HasSuffix(s, "\n") {
		n++
	}
	return n
}
