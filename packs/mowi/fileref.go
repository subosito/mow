package mowi

import (
	"os"
	"regexp"
	"strings"
)

// maxRefBytes caps how much of a referenced file is inlined.
const maxRefBytes = 100_000

// fileRefRe matches @path tokens (letters/digits and common path chars).
var fileRefRe = regexp.MustCompile(`@([A-Za-z0-9._/\-]+)`)

// pathResolver is the path jail used by @file expansion (mow.Engine.ResolvePath).
type pathResolver interface {
	ResolvePath(rel string) (string, error)
}

// expandFileRefs inlines @path references into the prompt sent to the model.
// Paths go through resolve (workspace + extra roots, same as FS tools). Returns
// the (possibly expanded) text and the attached ref list.
func expandFileRefs(resolve pathResolver, text string) (string, []string) {
	if resolve == nil || !strings.Contains(text, "@") {
		return text, nil
	}
	var attached []string
	seen := map[string]bool{}
	var b strings.Builder
	for _, mm := range fileRefRe.FindAllStringSubmatch(text, -1) {
		ref := strings.TrimRight(mm[1], ".,;:)")
		if ref == "" || seen[ref] {
			continue
		}
		abs, err := resolve.ResolvePath(ref)
		if err != nil {
			continue
		}
		fi, err := os.Stat(abs)
		if err != nil || fi.IsDir() {
			continue
		}
		data, err := os.ReadFile(abs)
		if err != nil {
			continue
		}
		if len(data) > maxRefBytes {
			data = append(data[:maxRefBytes], []byte("\n… (truncated)")...)
		}
		seen[ref] = true
		attached = append(attached, ref)
		b.WriteString("\n\n--- " + ref + " ---\n```" + resolveLangLabel(ref) + "\n")
		b.Write(data)
		b.WriteString("\n```")
	}
	if len(attached) == 0 {
		return text, nil
	}
	return text + "\n" + b.String(), attached
}
