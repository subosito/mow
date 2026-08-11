package mowi

import (
	"path/filepath"
	"strings"
)

// diffLineOp is the role of one parsed unified-diff line.
type diffLineOp int

const (
	dOpCtx diffLineOp = iota
	dOpAdd
	dOpDel
	dOpHunk
	dOpNote // "\ No newline…", fold ellipsis, raw meta
)

// diffModel is the structured intermediate form of a unified diff.
//
// Entry text arrives as plain unified-diff bytes (tool results). Parsing once
// lets unified and split painters share the same numbers, runs, and stats
// without re-walking the text and without importing an external diff package.
type diffModel struct {
	// Path is a file path when known (entry title or ---/+++ headers).
	Path string
	// Lines is the body after ---/+++/diff/index headers are dropped.
	Lines []diffModelLine
	Adds  int
	Dels  int
}

// diffModelLine is one line of a parsed unified diff.
type diffModelLine struct {
	Op   diffLineOp
	Text string // body without the leading +/−/space; hunk header raw for dOpHunk
	// Line numbers (1-based). Zero means "this side has no number".
	OldNum, NewNum int
	// Hunk ranges when Op == dOpHunk.
	OldH, NewH hunkRange
	HunkOK     bool
}

// parseUnifiedDiff builds a diffModel from unified-diff text (with or without
// a leading "edited path" title line — callers strip that).
func parseUnifiedDiff(code string) diffModel {
	code = strings.TrimRight(code, "\n")
	var d diffModel
	if code == "" {
		return d
	}
	oldLn, newLn := 1, 1
	haveNums := false
	for _, line := range strings.Split(code, "\n") {
		switch {
		case strings.HasPrefix(line, "--- "):
			if p := pathFromDiffHeader(line); p != "" && d.Path == "" {
				d.Path = p
			}
			continue
		case strings.HasPrefix(line, "+++ "):
			if p := pathFromDiffHeader(line); p != "" {
				d.Path = p // prefer the new-side path
			}
			continue
		case strings.HasPrefix(line, "diff "), strings.HasPrefix(line, "index "):
			continue
		}

		if strings.HasPrefix(line, "@@") {
			oh, nh, ok := parseHunkHeader(line)
			ml := diffModelLine{Op: dOpHunk, Text: strings.TrimSpace(line), OldH: oh, NewH: nh, HunkOK: ok}
			if ok {
				oldLn, newLn = oh.start, nh.start
				if oldLn == 0 && oh.count == 0 {
					oldLn = 0
				}
				if newLn == 0 && nh.count == 0 {
					newLn = 0
				}
				haveNums = true
			} else if strings.TrimSpace(line) == "@@" {
				oldLn, newLn = 1, 1
				haveNums = true
			}
			d.Lines = append(d.Lines, ml)
			continue
		}

		switch {
		case strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "---"):
			ml := diffModelLine{Op: dOpDel, Text: strings.TrimPrefix(line, "-")}
			if haveNums {
				ml.OldNum = oldLn
				oldLn++
			}
			d.Lines = append(d.Lines, ml)
			d.Dels++
		case strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++"):
			ml := diffModelLine{Op: dOpAdd, Text: strings.TrimPrefix(line, "+")}
			if haveNums {
				ml.NewNum = newLn
				newLn++
			}
			d.Lines = append(d.Lines, ml)
			d.Adds++
		case strings.HasPrefix(line, "\\"):
			d.Lines = append(d.Lines, diffModelLine{Op: dOpNote, Text: "no newline at end of file"})
		case strings.HasPrefix(line, "…") || strings.HasPrefix(line, "..."):
			d.Lines = append(d.Lines, diffModelLine{Op: dOpNote, Text: strings.TrimSpace(line)})
		default:
			body := line
			if strings.HasPrefix(body, " ") {
				body = body[1:]
			}
			ml := diffModelLine{Op: dOpCtx, Text: body}
			if haveNums {
				ml.OldNum = oldLn
				ml.NewNum = newLn
				oldLn++
				newLn++
			}
			d.Lines = append(d.Lines, ml)
		}
	}
	return d
}

// pathFromDiffHeader extracts a path from "--- a/foo" / "+++ b/foo" / "--- foo".
func pathFromDiffHeader(line string) string {
	// "--- a/path" or "+++ b/path\t…"
	rest := strings.TrimSpace(line)
	if len(rest) < 5 {
		return ""
	}
	rest = strings.TrimSpace(rest[4:]) // drop --- / +++
	if i := strings.IndexAny(rest, "\t"); i >= 0 {
		rest = rest[:i]
	}
	rest = strings.TrimSpace(rest)
	if rest == "/dev/null" {
		return ""
	}
	if strings.HasPrefix(rest, "a/") || strings.HasPrefix(rest, "b/") {
		rest = rest[2:]
	}
	return rest
}

// parseDiffEntry splits a kindDiff entry ("edited path\n<body>") into
// verb, path, and unified body.
func parseDiffEntry(text string) (op, path, body string) {
	text = strings.TrimRight(text, "\n")
	if text == "" {
		return "", "", ""
	}
	lines := strings.Split(text, "\n")
	head := lines[0]
	op, path, _ = strings.Cut(head, " ")
	path = strings.TrimSpace(path)
	if len(lines) > 1 {
		body = strings.Join(lines[1:], "\n")
	}
	return op, path, body
}

// splitDiffMinWidth is the narrowest terminal that can hold two useful panes.
// Below this the split painter falls back to unified so gutters and code stay
// readable (display-cell geometry, not byte length).
const splitDiffMinWidth = 88

// splitColMinWidth is the minimum cells for one side of a split row
// (gutter + a few body cells). Below this, fall back to unified.
const splitColMinWidth = 28

// expandDiffTabs expands tabs to spaces at tabWidth for display-cell math.
// Unified-diff bodies often carry real source tabs; cell width must match what
// the terminal paints, not raw rune counts.
func expandDiffTabs(s string, tabWidth int) string {
	if tabWidth < 1 {
		tabWidth = 4
	}
	if !strings.ContainsRune(s, '\t') {
		return s
	}
	var b strings.Builder
	col := 0
	for _, r := range s {
		if r == '\t' {
			n := tabWidth - (col % tabWidth)
			b.WriteString(strings.Repeat(" ", n))
			col += n
			continue
		}
		b.WriteRune(r)
		// Wide glyphs: approximate with 1; lipgloss/xansi handle true width later.
		col++
	}
	return b.String()
}

// diffBasename is the short name for titles and lexer matching.
func diffBasename(path string) string {
	if path == "" {
		return ""
	}
	return filepath.Base(path)
}
