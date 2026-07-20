package tools

import (
	"fmt"
	"strings"
)

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

// formatReplaceDiff reports an overwrite (write over existing) or full-file swap.
func formatReplaceDiff(path, oldContent, newContent string) string {
	var b strings.Builder
	b.WriteString("wrote " + path + "\n")
	b.WriteString("--- " + path + "\n")
	b.WriteString("+++ " + path + "\n")
	oldLines := splitLines(oldContent)
	newLines := splitLines(newContent)
	b.WriteString(fmt.Sprintf("@@ -1,%d +1,%d @@\n", len(oldLines), len(newLines)))
	// Simple full-file replace view (not LCS) — clear for agents and UIs.
	// Prefer showing new content when both sides are large.
	half := maxDiffBodyLines / 2
	if len(oldLines)+len(newLines) <= maxDiffBodyLines {
		writePrefixed(&b, "-", oldLines, len(oldLines))
		writePrefixed(&b, "+", newLines, len(newLines))
	} else {
		writePrefixed(&b, "-", oldLines, half)
		writePrefixed(&b, "+", newLines, half)
	}
	return b.String()
}

// formatEditDiff reports a search-replace edit with path and the changed hunk.
func formatEditDiff(path, oldString, newString string) string {
	var b strings.Builder
	b.WriteString("edited " + path + "\n")
	b.WriteString("--- " + path + "\n")
	b.WriteString("+++ " + path + "\n")
	oldLines := splitLines(oldString)
	newLines := splitLines(newString)
	b.WriteString("@@\n")
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
