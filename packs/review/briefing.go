package review

import (
	"fmt"
	"strings"
)

// scopeBriefing tells the model exactly what was selected and why, including
// the fact that scope was decided by the harness and is not negotiable.
func scopeBriefing(sc *Scope) string {
	var b strings.Builder
	b.WriteString("## Reviewed scope\n\n")
	fmt.Fprintf(&b, "- selection: %s\n", scopeModeDescription(sc))
	if sc.Git.Available {
		if sc.Git.Commit != "" {
			fmt.Fprintf(&b, "- commit: %s", sc.Git.Commit)
			if sc.Git.Branch != "" {
				fmt.Fprintf(&b, " (branch %s)", sc.Git.Branch)
			}
			b.WriteString("\n")
		}
	}
	fmt.Fprintf(&b, "- files in scope: %d\n", len(sc.Files))
	if len(sc.Excluded) > 0 {
		fmt.Fprintf(&b, "- files excluded: %d (vendor/generated/binary/over-budget)\n", len(sc.Excluded))
	}
	if sc.Truncated {
		fmt.Fprintf(&b, "- NOTE: scope was truncated (%s). Do not claim the change is complete or safe overall.\n", sc.TruncReason)
	}
	b.WriteString("\nThe file list below is the review scope. Every finding must cite one of these files. ")
	b.WriteString("You may read other files for context, but do not report findings about code outside the scope.\n")
	return b.String()
}

// scopeModeDescription renders the selection in user terms.
func scopeModeDescription(sc *Scope) string {
	switch sc.Mode {
	case "diff":
		return "git diff " + sc.Selector
	case "staged":
		return "staged changes (git diff --cached)"
	case "base":
		return "changes relative to " + sc.Selector
	case "worktree":
		return "uncommitted changes in the working tree"
	case "paths":
		return "explicit paths: " + sc.Selector
	default:
		return sc.Selector
	}
}

// scopeContent renders per-file diffs and content for the model. Diffs come
// first because in a change review the diff is the primary subject.
func scopeContent(sc *Scope) string {
	var b strings.Builder
	b.WriteString("## Files\n\n")
	for _, f := range sc.Files {
		fmt.Fprintf(&b, "### %s (%d lines)\n\n", f.Path, f.Lines)
		if f.Diff != "" {
			b.WriteString("Diff:\n\n```diff\n")
			b.WriteString(f.Diff)
			if !strings.HasSuffix(f.Diff, "\n") {
				b.WriteString("\n")
			}
			b.WriteString("```\n\n")
		}
		if f.Content != "" {
			fmt.Fprintf(&b, "Full content with line numbers:\n\n```\n%s```\n\n", numberLines(f.Content))
		} else if f.Diff == "" {
			b.WriteString("(content omitted — use the read tool if you need it)\n\n")
		}
	}
	return b.String()
}

// numberLines prefixes each line with its 1-based number so the model can cite
// accurate line ranges instead of estimating them.
func numberLines(s string) string {
	lines := strings.Split(strings.TrimSuffix(s, "\n"), "\n")
	var b strings.Builder
	width := len(fmt.Sprint(len(lines)))
	for i, ln := range lines {
		fmt.Fprintf(&b, "%*d| %s\n", width, i+1, ln)
	}
	return b.String()
}
