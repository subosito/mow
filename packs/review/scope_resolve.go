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
	return resolveScope(ctx, req, runGit, nil)
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
	rf := readFile
	if rf == nil {
		rf = workspaceReadFile(abs)
	}
	gather(sc, req, candidates, excludes, rf, git, ctx)
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("review: scope resolution cancelled")
	}
	return sc, nil
}

// workspaceReadFile adapts readWorkspaceFile to the absolute-path readFileFunc
// used by gather (and tests that inject memFS).
func workspaceReadFile(workspace string) readFileFunc {
	ws := filepath.Clean(workspace)
	return func(abs string) ([]byte, error) {
		rel, err := filepath.Rel(ws, abs)
		if err != nil {
			return nil, fmt.Errorf("review: %w", err)
		}
		return readWorkspaceFile(ws, filepath.ToSlash(rel))
	}
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
		if sc.Git.Available {
			rels, err := gitPathArgs(sc.Workspace, req.Paths)
			if err != nil {
				return nil, err
			}
			return gitIndexFiles(ctx, sc.Workspace, git, rels)
		}
		files, skipped, truncated, err := expandPaths(sc.Workspace, req.Paths, walkExcludes(req))
		recordSkippedDirs(sc, skipped)
		if truncated {
			markExpandCap(sc)
		}
		return files, err

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
		if sc.Git.Available {
			return gitIndexFiles(ctx, sc.Workspace, git, nil)
		}
		files, skipped, truncated, err := expandPaths(sc.Workspace, []string{"."}, walkExcludes(req))
		recordSkippedDirs(sc, skipped)
		if truncated {
			markExpandCap(sc)
		}
		return files, err
	}
}

// gitPathArgs turns user path args into workspace-relative git pathspecs.
// "." means the whole tree (no pathspecs). Escaping paths error.
func gitPathArgs(workspace string, paths []string) ([]string, error) {
	var rels []string
	whole := false
	for _, p := range paths {
		rel, err := NormalizePath(p, workspace)
		if err != nil {
			if strings.TrimSpace(p) == "." || strings.TrimSpace(p) == "./" {
				whole = true
				continue
			}
			return nil, fmt.Errorf("review: %w", err)
		}
		if rel == "." {
			whole = true
			continue
		}
		full := filepath.Join(workspace, filepath.FromSlash(rel))
		if _, err := os.Lstat(full); err != nil {
			return nil, fmt.Errorf("review: %s: %w", rel, err)
		}
		rels = append(rels, rel)
	}
	if whole {
		return nil, nil
	}
	return rels, nil
}

func walkExcludes(req ScopeRequest) []string {
	if req.IncludeAll {
		return req.Excludes
	}
	return append(append([]string(nil), DefaultExcludes()...), req.Excludes...)
}

func markExpandCap(sc *Scope) {
	if sc == nil {
		return
	}
	sc.Truncated = true
	if sc.TruncReason == "" {
		sc.TruncReason = fmt.Sprintf("path walk reached %d file cap", maxExpandCandidates)
	}
}

func recordSkippedDirs(sc *Scope, skipped []ExcludedFile) {
	for _, e := range skipped {
		recordExcluded(sc, e.Path, e.Reason)
	}
}

// expandPaths walks explicit files/directories into a sorted file list.
// excludes, when non-empty, SkipDir directories whose children would match
// (node_modules, vendor, …) so a walk cap cannot hide source behind a
// default-excluded tree. IncludeAll passes only caller excludes.
func expandPaths(workspace string, paths, excludes []string) ([]string, []ExcludedFile, bool, error) {
	var out []string
	var skipped []ExcludedFile
	seen := map[string]bool{}
	truncated := false
	for _, p := range paths {
		if truncated {
			break
		}
		rel, err := NormalizePath(p, workspace)
		if err != nil {
			// "." means the whole workspace.
			if strings.TrimSpace(p) == "." || strings.TrimSpace(p) == "./" {
				rel = "."
			} else {
				return nil, nil, false, fmt.Errorf("review: %w", err)
			}
		}
		full := filepath.Join(workspace, filepath.FromSlash(rel))
		info, err := os.Lstat(full)
		if err != nil {
			return nil, nil, false, fmt.Errorf("review: %s: %w", rel, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return nil, nil, false, fmt.Errorf("review: %s: cannot review a symlink path", rel)
		}
		if !info.IsDir() {
			if !seen[rel] {
				seen[rel] = true
				out = append(out, rel)
			}
			if len(out) > maxExpandCandidates {
				out = out[:maxExpandCandidates]
				truncated = true
			}
			continue
		}
		err = filepath.WalkDir(full, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil // unreadable entries are skipped, not fatal
			}
			if d.Type()&os.ModeSymlink != 0 {
				if d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if d.IsDir() {
				// Only .git is pruned here: it is repository metadata, never
				// reviewable source. vendor/node_modules are SkipDir'd when they
				// match walk excludes so a file-count cap cannot hide src/
				// behind a vendored tree; --include-all still descends them.
				if d.Name() == ".git" {
					return filepath.SkipDir
				}
				if r, rerr := filepath.Rel(workspace, p); rerr == nil {
					r = filepath.ToSlash(r)
					if pat, ok := skipExcludedDir(r, excludes); ok {
						skipped = append(skipped, ExcludedFile{
							Path:   r,
							Reason: "excluded by " + pat,
						})
						return filepath.SkipDir
					}
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
			if len(out) > maxExpandCandidates {
				out = out[:maxExpandCandidates]
				truncated = true
				return fs.SkipAll
			}
			return nil
		})
		if err != nil {
			return nil, nil, false, fmt.Errorf("review: walk %s: %w", rel, err)
		}
	}
	sort.Strings(out)
	return out, skipped, truncated, nil
}

func skipExcludedDir(rel string, excludes []string) (string, bool) {
	if rel == "" || rel == "." || len(excludes) == 0 {
		return "", false
	}
	// Directory patterns are "name/**"; the dir itself does not match, a child does.
	return MatchAny(excludes, rel+"/x")
}

func recordExcluded(sc *Scope, rel, reason string) {
	if sc == nil {
		return
	}
	if len(sc.Excluded) >= maxExcludedFiles {
		return
	}
	if len(sc.Excluded) == maxExcludedFiles-1 {
		sc.Excluded = append(sc.Excluded, ExcludedFile{
			Path:   "…",
			Reason: fmt.Sprintf("skip list capped at %d entries", maxExcludedFiles),
		})
		return
	}
	sc.Excluded = append(sc.Excluded, ExcludedFile{Path: rel, Reason: reason})
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
		if err := ctx.Err(); err != nil {
			return
		}
		if len(sc.Files) >= sc.Budget.MaxFiles && len(sc.Excluded) >= maxExcludedFiles {
			return
		}
		if pat, hit := MatchAny(excludes, rel); hit {
			recordExcluded(sc, rel, "excluded by "+pat)
			continue
		}
		if len(sc.Files) >= sc.Budget.MaxFiles {
			recordExcluded(sc, rel, "over budget (max files)")
			sc.Truncated = true
			sc.TruncReason = fmt.Sprintf("budget %s: file limit %d reached", sc.Budget.Name, sc.Budget.MaxFiles)
			continue
		}
		full := filepath.Join(sc.Workspace, filepath.FromSlash(rel))
		raw, err := readFile(full)
		if err != nil {
			recordExcluded(sc, rel, "unreadable: "+err.Error())
			continue
		}
		if len(raw) > sc.Budget.MaxFileBytes {
			recordExcluded(sc, rel, fmt.Sprintf("file too large (%d bytes)", len(raw)))
			continue
		}
		if isBinary(raw) {
			recordExcluded(sc, rel, "binary file")
			continue
		}
		text := string(raw)
		if !req.IncludeAll && LooksGenerated(head(text)) {
			recordExcluded(sc, rel, "generated file")
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
				recordExcluded(sc, rel, "over budget (max bytes)")
				sc.Truncated = true
				sc.TruncReason = fmt.Sprintf("budget %s: byte limit %d reached", sc.Budget.Name, sc.Budget.MaxBytes)
				continue
			}
		}
		if f.Diff == "" && f.Content == "" {
			// Nothing to show the model: don't spend a scope slot on it.
			recordExcluded(sc, rel, "no diff or content available")
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
