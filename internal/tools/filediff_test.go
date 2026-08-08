package tools

import (
	"fmt"
	"strings"
	"testing"
)

// diffBody returns the +/- lines of a diff (excluding file headers).
func diffBody(out string) []string {
	var o []string
	for _, l := range strings.Split(out, "\n") {
		if strings.HasPrefix(l, "+++") || strings.HasPrefix(l, "---") {
			continue
		}
		if strings.HasPrefix(l, "+") || strings.HasPrefix(l, "-") {
			o = append(o, l)
		}
	}
	return o
}

// applyDiff reconstructs the new file from the old one plus the diff, which is
// the property that actually matters: a diff that does not reproduce the new
// content is wrong no matter how small it is.
func applyDiff(t *testing.T, oldContent, diff string) string {
	t.Helper()
	oldLines := splitLines(oldContent)
	var out []string
	oldPos := 0 // 0-based index into oldLines
	for _, line := range strings.Split(diff, "\n") {
		switch {
		case strings.HasPrefix(line, "--- ") || strings.HasPrefix(line, "+++ "):
			continue
		case strings.HasPrefix(line, "@@"):
			var os, oc, ns, nc int
			if _, err := fmt.Sscanf(line, "@@ -%d,%d +%d,%d @@", &os, &oc, &ns, &nc); err != nil {
				continue
			}
			// Copy untouched lines between the last hunk and this one.
			for oldPos < os-1 && oldPos < len(oldLines) {
				out = append(out, oldLines[oldPos])
				oldPos++
			}
		case strings.HasPrefix(line, " "):
			out = append(out, line[1:])
			oldPos++
		case strings.HasPrefix(line, "-"):
			oldPos++
		case strings.HasPrefix(line, "+"):
			out = append(out, line[1:])
		}
	}
	// Trailing untouched lines.
	for oldPos < len(oldLines) {
		out = append(out, oldLines[oldPos])
		oldPos++
	}
	return strings.Join(out, "\n")
}

func TestDiffLinesRoundTrip(t *testing.T) {
	cases := []struct {
		name     string
		old, neu []string
	}{
		{"identical", []string{"a", "b", "c"}, []string{"a", "b", "c"}},
		{"one_change_middle", []string{"a", "b", "c"}, []string{"a", "X", "c"}},
		{"insert_only", []string{"a", "c"}, []string{"a", "b", "c"}},
		{"delete_only", []string{"a", "b", "c"}, []string{"a", "c"}},
		{"prepend", []string{"b"}, []string{"a", "b"}},
		{"append", []string{"a"}, []string{"a", "b"}},
		{"empty_to_content", []string{}, []string{"a", "b"}},
		{"content_to_empty", []string{"a", "b"}, []string{}},
		{"total_rewrite", []string{"a", "b"}, []string{"x", "y"}},
		{"dup_lines", []string{"a", "a", "a"}, []string{"a", "a"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ops := diffLines(tc.old, tc.neu)
			// Replaying the script must reproduce both sides exactly.
			var gotOld, gotNew []string
			for _, op := range ops {
				switch op.kind {
				case ' ':
					gotOld = append(gotOld, op.text)
					gotNew = append(gotNew, op.text)
				case '-':
					gotOld = append(gotOld, op.text)
				case '+':
					gotNew = append(gotNew, op.text)
				}
			}
			if strings.Join(gotOld, "\n") != strings.Join(tc.old, "\n") {
				t.Fatalf("old side not reproduced:\ngot  %q\nwant %q", gotOld, tc.old)
			}
			if strings.Join(gotNew, "\n") != strings.Join(tc.neu, "\n") {
				t.Fatalf("new side not reproduced:\ngot  %q\nwant %q", gotNew, tc.neu)
			}
		})
	}
}

// The whole point: a small edit to a large file must report a small diff.
func TestReplaceDiffIsMinimal(t *testing.T) {
	lines := make([]string, 60)
	for i := range lines {
		lines[i] = fmt.Sprintf("line %d", i)
	}
	old := strings.Join(lines, "\n")
	changed := append([]string(nil), lines...)
	changed[30] = "CHANGED"
	out := formatReplaceDiff("f.go", old, strings.Join(changed, "\n"))

	body := diffBody(out)
	if len(body) != 2 {
		t.Fatalf("one-line change produced %d changed lines, want 2:\n%s", len(body), out)
	}
	// And it must still reconstruct correctly.
	if got := applyDiff(t, old, out); got != strings.Join(changed, "\n") {
		t.Fatalf("diff does not reconstruct the new content:\n%s", out)
	}
}

// Diffs must reconstruct the new file for realistic edit shapes.
func TestReplaceDiffReconstructs(t *testing.T) {
	base := make([]string, 50)
	for i := range base {
		base[i] = fmt.Sprintf("line %d", i)
	}
	mut := func(f func([]string) []string) []string { return f(append([]string(nil), base...)) }

	cases := []struct {
		name string
		neu  []string
	}{
		{"single", mut(func(s []string) []string { s[10] = "X"; return s })},
		{"two_far_apart", mut(func(s []string) []string { s[5] = "X"; s[45] = "Y"; return s })},
		{"adjacent", mut(func(s []string) []string { s[20] = "X"; s[21] = "Y"; return s })},
		{"delete_block", append(append([]string{}, base[:10]...), base[20:]...)},
		{"insert_block", append(append(append([]string{}, base[:10]...), "N1", "N2"), base[10:]...)},
		{"head_edit", mut(func(s []string) []string { s[0] = "X"; return s })},
		{"tail_edit", mut(func(s []string) []string { s[len(s)-1] = "X"; return s })},
	}
	oldStr := strings.Join(base, "\n")
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			want := strings.Join(tc.neu, "\n")
			out := formatReplaceDiff("f.go", oldStr, want)
			if got := applyDiff(t, oldStr, out); got != want {
				t.Fatalf("reconstruction mismatch:\n--- got ---\n%s\n--- want ---\n%s\n--- diff ---\n%s", got, want, out)
			}
		})
	}
}

// Hunk headers drive line numbers in UIs, so their ranges must match the body.
func TestReplaceDiffHunkHeadersMatchBody(t *testing.T) {
	base := make([]string, 40)
	for i := range base {
		base[i] = fmt.Sprintf("line %d", i)
	}
	neu := append([]string(nil), base...)
	neu[5] = "X"
	neu[35] = "Y"
	out := formatReplaceDiff("f.go", strings.Join(base, "\n"), strings.Join(neu, "\n"))

	var oc, nc int
	var sawHeader bool
	check := func() {
		if !sawHeader {
			return
		}
		if oc != 0 || nc != 0 {
			t.Fatalf("hunk counts disagree with body (old off by %d, new off by %d):\n%s", oc, nc, out)
		}
	}
	for _, line := range strings.Split(out, "\n") {
		switch {
		case strings.HasPrefix(line, "@@"):
			check()
			var os, ns int
			if _, err := fmt.Sscanf(line, "@@ -%d,%d +%d,%d @@", &os, &oc, &ns, &nc); err != nil {
				t.Fatalf("unparseable hunk header %q", line)
			}
			sawHeader = true
		case strings.HasPrefix(line, " "):
			oc--
			nc--
		case strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "---"):
			oc--
		case strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++"):
			nc--
		}
	}
	check()
}

// The body stays bounded no matter how large the change is.
func TestReplaceDiffStaysBounded(t *testing.T) {
	old := make([]string, 500)
	neu := make([]string, 500)
	for i := range old {
		old[i] = fmt.Sprintf("old %d", i)
		neu[i] = fmt.Sprintf("new %d", i)
	}
	out := formatReplaceDiff("f.go", strings.Join(old, "\n"), strings.Join(neu, "\n"))
	if n := len(diffBody(out)); n > maxDiffBodyLines+4 {
		t.Fatalf("unbounded diff body: %d lines", n)
	}
}

// A rewrite with identical content must not claim spurious changes.
func TestReplaceDiffIdenticalContent(t *testing.T) {
	s := "a\nb\nc"
	out := formatReplaceDiff("f.go", s, s)
	if body := diffBody(out); len(body) != 0 {
		t.Fatalf("identical content reported changes: %v\n%s", body, out)
	}
}
