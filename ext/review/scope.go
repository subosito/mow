package review

import (
	"strings"
)

// Budget bounds how much code one review run may pull in. Budgets exist so a
// user never has to reason about turns or tokens: they pick a size, and mow
// enforces file/byte caps and truncates the scope deterministically.
type Budget struct {
	Name string
	// MaxFiles caps the number of reviewed files.
	MaxFiles int
	// MaxBytes caps the total size of gathered file/diff content.
	MaxBytes int
	// MaxFileBytes skips individual files larger than this (minified blobs,
	// huge fixtures) that would eat the whole budget.
	MaxFileBytes int
	// MaxTurns is the agent turn cap for each workflow pass.
	MaxTurns int
}

// Budgets are the named sizes accepted by --budget.
//
// MaxTurns has to cover exploration *and* the final JSON answer. Dogfooding
// calibrated both ends: left uncapped, a capable model spends 20+ turns
// re-reading files it already has; capped too tightly (12), it spends the
// budget exploring and never emits its report, which surfaces as a failed run.
// These values leave room to answer after a normal amount of looking around.
func Budgets() map[string]Budget {
	return map[string]Budget{
		"small":  {Name: "small", MaxFiles: 15, MaxBytes: 120_000, MaxFileBytes: 40_000, MaxTurns: 30},
		"medium": {Name: "medium", MaxFiles: 40, MaxBytes: 400_000, MaxFileBytes: 80_000, MaxTurns: 45},
		"large":  {Name: "large", MaxFiles: 120, MaxBytes: 1_200_000, MaxFileBytes: 160_000, MaxTurns: 70},
	}
}

// BudgetNames lists budgets from smallest to largest.
func BudgetNames() []string { return []string{"small", "medium", "large"} }

// LookupBudget resolves a budget name (empty → medium).
func LookupBudget(name string) (Budget, bool) {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" {
		name = "medium"
	}
	b, ok := Budgets()[name]
	return b, ok
}

// ScopeRequest is what the user asked to review, before resolution.
type ScopeRequest struct {
	// Workspace is the review root (absolute).
	Workspace string
	// Paths are explicit files or directories (workspace-relative or absolute).
	Paths []string
	// Diff is a git revision range, e.g. "main...HEAD".
	Diff string
	// Staged reviews the index (git diff --cached).
	Staged bool
	// Base diffs the working tree against a base ref, e.g. "origin/main".
	Base string
	// Excludes are extra globs on top of DefaultExcludes.
	Excludes []string
	// IncludeAll disables the default exclude list (vendor, generated, …).
	IncludeAll bool
	// Budget names the size cap; empty means "medium".
	Budget string
	// DiffContextLines is the -U value for gathered diffs.
	DiffContextLines int
}

// ScopeFile is one in-scope file with the content the workflow will show.
type ScopeFile struct {
	Path  string `json:"path"`
	Bytes int    `json:"bytes"`
	Lines int    `json:"lines"`
	// Diff is the unified diff for this file when the scope is diff-based.
	Diff string `json:"-"`
	// Content is the full file text when the scope is path-based (or the file
	// is small enough to include alongside its diff).
	Content string `json:"-"`
}

// ExcludedFile records a skipped path and why, so the scope header can explain
// itself instead of silently shrinking the review.
type ExcludedFile struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

// Scope is the resolved review scope: exactly what mow will show the model.
type Scope struct {
	Workspace   string
	Profile     string
	Mode        string // "diff", "staged", "base", "paths", "worktree"
	Selector    string // git range or equivalent description
	Files       []ScopeFile
	Excluded    []ExcludedFile
	Git         GitContext
	Budget      Budget
	Truncated   bool
	TruncReason string
	TotalBytes  int
	// index is the set of in-scope paths for validation lookups.
	index map[string]int
}

// Empty reports whether there is nothing to review.
func (s *Scope) Empty() bool { return s == nil || len(s.Files) == 0 }

// InScope reports whether a workspace-relative path was reviewed.
func (s *Scope) InScope(rel string) bool {
	if s == nil || s.index == nil {
		return false
	}
	_, ok := s.index[rel]
	return ok
}

// FileLines returns the line count of an in-scope file.
func (s *Scope) FileLines(rel string) (int, bool) {
	if s == nil || s.index == nil {
		return 0, false
	}
	i, ok := s.index[rel]
	if !ok {
		return 0, false
	}
	return s.Files[i].Lines, true
}

// Paths lists in-scope files in review order.
func (s *Scope) Paths() []string {
	out := make([]string, 0, len(s.Files))
	for _, f := range s.Files {
		out = append(out, f.Path)
	}
	return out
}

// Info projects the scope into the report envelope.
func (s *Scope) Info(req ScopeRequest) ScopeInfo {
	info := ScopeInfo{
		Mode:      s.Mode,
		Selection: s.Selector,
		Paths:     append([]string(nil), req.Paths...),
		// Diff carries the git range only when one was actually used; in
		// path/worktree mode there is no range and reporting the selector here
		// would describe a diff that does not exist.
		Staged:        req.Staged,
		Base:          req.Base,
		Files:         s.Paths(),
		FilesReviewed: len(s.Files),
		FilesExcluded: len(s.Excluded),
		Budget:        s.Budget.Name,
	}
	if s.Mode == "diff" || s.Mode == "base" {
		info.Diff = s.Selector
	}
	for _, e := range s.Excluded {
		info.Excluded = append(info.Excluded, e.Path)
	}
	return info
}
