package tools

import (
	"fmt"
	"strings"
)

// diffContextLines is the unified-diff context kept around each change.
const diffContextLines = 3

// maxDiffHunks caps how many separate hunks a replace diff reports. A rewrite
// that touches the whole file degrades to many tiny hunks, which is noisier
// than saying so once.
const maxDiffHunks = 12

// diffOp is one edit-script entry over whole lines.
type diffOp struct {
	kind byte // ' ' context, '-' delete, '+' insert
	text string
}

// diffLines computes a line-level edit script via LCS.
//
// The table is O(n·m); callers guard with lcsBudget and fall back to a
// whole-file replace view when the inputs are too large to be worth it.
func diffLines(a, b []string) []diffOp {
	n, m := len(a), len(b)
	// Trim the common prefix/suffix first: real edits touch a small middle,
	// and this keeps the LCS table small enough to matter.
	pre := 0
	for pre < n && pre < m && a[pre] == b[pre] {
		pre++
	}
	suf := 0
	for suf < n-pre && suf < m-pre && a[n-1-suf] == b[m-1-suf] {
		suf++
	}
	midA, midB := a[pre:n-suf], b[pre:m-suf]

	ops := make([]diffOp, 0, n+m)
	for _, s := range a[:pre] {
		ops = append(ops, diffOp{' ', s})
	}

	// LCS over the differing middle.
	na, nb := len(midA), len(midB)
	if na > 0 || nb > 0 {
		lcs := make([][]int, na+1)
		for i := range lcs {
			lcs[i] = make([]int, nb+1)
		}
		for i := na - 1; i >= 0; i-- {
			for j := nb - 1; j >= 0; j-- {
				if midA[i] == midB[j] {
					lcs[i][j] = lcs[i+1][j+1] + 1
				} else {
					lcs[i][j] = max(lcs[i+1][j], lcs[i][j+1])
				}
			}
		}
		i, j := 0, 0
		for i < na && j < nb {
			switch {
			case midA[i] == midB[j]:
				ops = append(ops, diffOp{' ', midA[i]})
				i++
				j++
			case lcs[i+1][j] >= lcs[i][j+1]:
				ops = append(ops, diffOp{'-', midA[i]})
				i++
			default:
				ops = append(ops, diffOp{'+', midB[j]})
				j++
			}
		}
		for ; i < na; i++ {
			ops = append(ops, diffOp{'-', midA[i]})
		}
		for ; j < nb; j++ {
			ops = append(ops, diffOp{'+', midB[j]})
		}
	}

	for _, s := range a[n-suf:] {
		ops = append(ops, diffOp{' ', s})
	}
	return ops
}

// lcsBudget caps the LCS table size (cells). Beyond this the quadratic cost
// stops being worth the tokens saved, and the whole-file view is used instead.
const lcsBudget = 4_000_000

// hunk is a contiguous run of changes plus its surrounding context.
type hunk struct {
	oldStart, oldCount int
	newStart, newCount int
	ops                []diffOp
}

// buildHunks groups an edit script into unified-diff hunks with context.
func buildHunks(ops []diffOp, ctx int) []hunk {
	// Mark which ops are near a change.
	keep := make([]bool, len(ops))
	for i, op := range ops {
		if op.kind == ' ' {
			continue
		}
		lo := max(0, i-ctx)
		hi := min(len(ops)-1, i+ctx)
		for j := lo; j <= hi; j++ {
			keep[j] = true
		}
	}

	var out []hunk
	oldLn, newLn := 1, 1
	i := 0
	for i < len(ops) {
		if !keep[i] {
			if ops[i].kind != '+' {
				oldLn++
			}
			if ops[i].kind != '-' {
				newLn++
			}
			i++
			continue
		}
		h := hunk{oldStart: oldLn, newStart: newLn}
		for i < len(ops) && keep[i] {
			op := ops[i]
			h.ops = append(h.ops, op)
			if op.kind != '+' {
				h.oldCount++
				oldLn++
			}
			if op.kind != '-' {
				h.newCount++
				newLn++
			}
			i++
		}
		out = append(out, h)
	}
	return out
}

// maxDiffBodyLines caps tool-result diffs so the model context stays bounded.
const maxDiffBodyLines = 80

// formatCreateDiff reports a new file write with path + added lines.
func formatCreateDiff(path, content string) string {
	var b strings.Builder
	b.WriteString("created " + path + "\n")
	b.WriteString("--- /dev/null\n")
	b.WriteString("+++ " + path + "\n")
	lines := splitLines(content)
	b.WriteString(fmt.Sprintf("@@ -0,0 +1,%d @@\n", len(lines)))
	writePrefixed(&b, "+", lines, maxDiffBodyLines)
	return b.String()
}

// formatReplaceDiff reports an overwrite (write over existing file).
//
// Emits a real line-level diff: an edit that changes one line of a large file
// reports that one line with context, not the whole file twice. This is the
// same text the model sees in the tool result, so a tighter diff is directly
// fewer tokens per write — and the accurate @@ ranges let UIs show true line
// numbers. Falls back to the whole-file view when the LCS would cost more than
// it saves, or when the rewrite is so total that hunks stop being meaningful.
func formatReplaceDiff(path, oldContent, newContent string) string {
	oldLines := splitLines(oldContent)
	newLines := splitLines(newContent)

	var b strings.Builder
	b.WriteString("wrote " + path + "\n")
	b.WriteString("--- " + path + "\n")
	b.WriteString("+++ " + path + "\n")

	if len(oldLines)*len(newLines) > lcsBudget {
		writeWholeFileBody(&b, oldLines, newLines)
		return b.String()
	}
	hunks := buildHunks(diffLines(oldLines, newLines), diffContextLines)
	switch {
	case len(hunks) == 0:
		// Content identical (e.g. rewritten with the same bytes).
		b.WriteString("@@ -1,0 +1,0 @@\n")
		b.WriteString("(no changes)\n")
		return b.String()
	case len(hunks) > maxDiffHunks:
		writeWholeFileBody(&b, oldLines, newLines)
		return b.String()
	}

	// Budget the body across hunks so a huge edit still stays bounded.
	remaining := maxDiffBodyLines
	for _, h := range hunks {
		if remaining <= 0 {
			b.WriteString("… (diff truncated)\n")
			break
		}
		fmt.Fprintf(&b, "@@ -%d,%d +%d,%d @@\n", h.oldStart, h.oldCount, h.newStart, h.newCount)
		for _, op := range h.ops {
			if remaining <= 0 {
				b.WriteString("… (diff truncated)\n")
				break
			}
			b.WriteByte(op.kind)
			b.WriteString(op.text)
			b.WriteByte('\n')
			remaining--
		}
	}
	return b.String()
}

// writeWholeFileBody is the pre-LCS view: old lines then new lines. Used when
// a real diff is not worth computing or would be all noise.
func writeWholeFileBody(b *strings.Builder, oldLines, newLines []string) {
	fmt.Fprintf(b, "@@ -1,%d +1,%d @@\n", len(oldLines), len(newLines))
	half := maxDiffBodyLines / 2
	if len(oldLines)+len(newLines) <= maxDiffBodyLines {
		writePrefixed(b, "-", oldLines, len(oldLines))
		writePrefixed(b, "+", newLines, len(newLines))
		return
	}
	writePrefixed(b, "-", oldLines, half)
	writePrefixed(b, "+", newLines, half)
}

// formatEditDiff reports a search-replace edit with path and the changed hunk.
// Emits a numbered @@ header so UIs can show line ranges (relative to the hunk;
// absolute file offsets are unknown for search-replace).
func formatEditDiff(path, oldString, newString string) string {
	var b strings.Builder
	b.WriteString("edited " + path + "\n")
	b.WriteString("--- " + path + "\n")
	b.WriteString("+++ " + path + "\n")
	oldLines := splitLines(oldString)
	newLines := splitLines(newString)
	oc, nc := len(oldLines), len(newLines)
	switch {
	case oc == 0 && nc == 0:
		b.WriteString("@@ -0,0 +0,0 @@\n")
	case oc == 0:
		b.WriteString(fmt.Sprintf("@@ -0,0 +1,%d @@\n", nc))
	case nc == 0:
		b.WriteString(fmt.Sprintf("@@ -1,%d +0,0 @@\n", oc))
	default:
		b.WriteString(fmt.Sprintf("@@ -1,%d +1,%d @@\n", oc, nc))
	}
	writePrefixed(&b, "-", oldLines, maxDiffBodyLines)
	writePrefixed(&b, "+", newLines, maxDiffBodyLines)
	return b.String()
}

func splitLines(s string) []string {
	if s == "" {
		return []string{}
	}
	// Keep a single trailing empty line only if the source ends with \n and has content.
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.TrimSuffix(s, "\n")
	if s == "" {
		return []string{""}
	}
	return strings.Split(s, "\n")
}

func writePrefixed(b *strings.Builder, prefix string, lines []string, limit int) {
	n := len(lines)
	show := n
	if show > limit {
		show = limit
	}
	for i := 0; i < show; i++ {
		b.WriteString(prefix)
		b.WriteString(lines[i])
		b.WriteByte('\n')
	}
	if n > show {
		b.WriteString(fmt.Sprintf("… (%d more lines)\n", n-show))
	}
}
