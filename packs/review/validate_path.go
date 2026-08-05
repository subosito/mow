package review

import (
	"fmt"
	"path"
	"strings"
)

// maxTextField bounds free-form model prose so one runaway field cannot blow up
// a report, a terminal, or a CI annotation.
const maxTextField = 4000

// ValidationOptions controls how strictly raw model output is checked.
type ValidationOptions struct {
	// Profile supplies the allowed taxonomy and profile-specific fields.
	Profile *Profile
	// InScope reports whether a workspace-relative path was part of the review
	// scope. Nil means "any existing path is acceptable".
	InScope func(rel string) bool
	// FileLines returns the line count for a workspace-relative path and
	// whether the file exists. Nil disables path/line existence checks.
	FileLines func(rel string) (int, bool)
	// AllowOutOfScope keeps findings whose path is outside the resolved scope
	// (demoted with a note) instead of dropping them.
	AllowOutOfScope bool
}

// ValidationIssue records why a candidate finding was rejected or adjusted.
type ValidationIssue struct {
	Index   int    `json:"index"`
	Title   string `json:"title,omitempty"`
	Reason  string `json:"reason"`
	Dropped bool   `json:"dropped"`
}

func (v ValidationIssue) String() string {
	verb := "adjusted"
	if v.Dropped {
		verb = "dropped"
	}
	if v.Title != "" {
		return fmt.Sprintf("finding %d (%s) %s: %s", v.Index, v.Title, verb, v.Reason)
	}
	return fmt.Sprintf("finding %d %s: %s", v.Index, verb, v.Reason)
}

// NormalizePath converts a model-supplied path into a clean workspace-relative
// path. Absolute paths, "./" prefixes, and backslashes are tolerated; anything
// escaping the workspace (".." or an unmappable absolute path) is rejected.
func NormalizePath(raw, workspace string) (string, error) {
	p := strings.TrimSpace(strings.ReplaceAll(raw, "\\", "/"))
	if p == "" {
		return "", fmt.Errorf("empty path")
	}
	// Trim a "path:line" suffix some models append.
	if i := strings.LastIndex(p, ":"); i > 0 {
		if _, err := fmt.Sscanf(p[i+1:], "%d", new(int)); err == nil {
			p = p[:i]
		}
	}
	ws := strings.TrimSuffix(strings.ReplaceAll(strings.TrimSpace(workspace), "\\", "/"), "/")
	if path.IsAbs(p) {
		if ws == "" {
			return "", fmt.Errorf("absolute path %q outside workspace", raw)
		}
		switch {
		case p == ws:
			return "", fmt.Errorf("path %q is the workspace root", raw)
		case strings.HasPrefix(p, ws+"/"):
			p = strings.TrimPrefix(p, ws+"/")
		default:
			return "", fmt.Errorf("absolute path %q outside workspace", raw)
		}
	}
	p = path.Clean(strings.TrimPrefix(p, "./"))
	if p == "." || p == "" {
		return "", fmt.Errorf("empty path")
	}
	if p == ".." || strings.HasPrefix(p, "../") {
		return "", fmt.Errorf("path %q escapes the workspace", raw)
	}
	return p, nil
}

// clampText trims whitespace and bounds length, marking truncation visibly.
func clampText(s string) string {
	s = strings.TrimSpace(s)
	if len(s) <= maxTextField {
		return s
	}
	return s[:maxTextField] + "… (truncated)"
}

// normalizeLines orders and bounds a line range against the real file length.
// Returns the corrected range plus an optional note when it had to be adjusted.
func normalizeLines(start, end, fileLines int) (int, int, string) {
	if start < 0 {
		start = 0
	}
	if end < 0 {
		end = 0
	}
	if start == 0 && end > 0 {
		start, end = end, 0
	}
	if end > 0 && end < start {
		start, end = end, start
	}
	if fileLines <= 0 {
		return start, end, ""
	}
	note := ""
	if start > fileLines {
		note = fmt.Sprintf("start_line %d beyond end of file (%d lines)", start, fileLines)
		start, end = 0, 0
	} else if end > fileLines {
		note = fmt.Sprintf("end_line %d clamped to %d", end, fileLines)
		end = fileLines
	}
	return start, end, note
}
