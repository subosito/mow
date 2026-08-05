package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeArchive creates <root>/<sess>.archive/<name> with body.
func writeArchive(t *testing.T, root, sess, name, body string) {
	t.Helper()
	dir := filepath.Join(root, sess+".archive")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func execSearch(t *testing.T, tool interface {
	Exec(context.Context, json.RawMessage) (string, error)
}, args string) string {
	t.Helper()
	out, err := tool.Exec(context.Background(), json.RawMessage(args))
	if err != nil {
		t.Fatalf("exec %s: %v", args, err)
	}
	return out
}

// Legacy {"pattern":"..."} callers keep working and get a stable file:line ref.
func TestContextSearchLegacyStringPattern(t *testing.T) {
	root := t.TempDir()
	writeArchive(t, root, "s1", "0001-a.md", "# archive\n## [user]\nremember marker-zeta-99\ntail\n")

	out := execSearch(t, NewContextSearch(root), `{"pattern":"marker-zeta-99"}`)
	if !strings.Contains(out, "marker-zeta-99") {
		t.Fatalf("miss: %q", out)
	}
	if !strings.Contains(out, "s1.archive/0001-a.md:3") {
		t.Fatalf("want stable file:line reference, got %q", out)
	}
	// Surrounding lines come along as context.
	if !strings.Contains(out, "## [user]") {
		t.Fatalf("want context lines, got %q", out)
	}
}

// A list of patterns matches any of them; a single hit line is reported once.
func TestContextSearchMultiPattern(t *testing.T) {
	root := t.TempDir()
	writeArchive(t, root, "s1", "0001-a.md",
		"alpha line\n\n\n\n\n\n\n\n\nbeta line\n\n\n\n\n\n\n\n\nboth alpha beta\n")

	out := execSearch(t, NewContextSearch(root), `{"pattern":["alpha","beta"],"context_lines":0}`)
	for _, want := range []string{"alpha line", "beta line", "both alpha beta"} {
		if !strings.Contains(out, want) {
			t.Fatalf("want %q in %q", want, out)
		}
	}
	if n := strings.Count(out, "both alpha beta"); n != 1 {
		t.Fatalf("multi-pattern line reported %d times: %q", n, out)
	}
}

// Nearby hits merge into one window instead of repeating overlapping lines.
func TestContextSearchMergesOverlappingHits(t *testing.T) {
	root := t.TempDir()
	writeArchive(t, root, "s1", "0001-a.md", "pad\nhit one\nmiddle\nhit two\npad\n")

	out := execSearch(t, NewContextSearch(root), `{"pattern":"hit"}`)
	if n := strings.Count(out, "middle"); n != 1 {
		t.Fatalf("overlapping snippets not merged (middle x%d): %q", n, out)
	}
	if n := strings.Count(out, "hit two"); n != 1 {
		t.Fatalf("hit two repeated: %q", out)
	}
}

// The same turn archived twice (auto compact + explicit Compact) is reported once.
func TestContextSearchDedupesAcrossFiles(t *testing.T) {
	root := t.TempDir()
	body := "## [user]\nduplicate-token here\ntail\n"
	writeArchive(t, root, "s1", "0001-a.md", body)
	writeArchive(t, root, "s2", "0001-a.md", body)

	out := execSearch(t, NewContextSearch(root), `{"pattern":"duplicate-token"}`)
	if n := strings.Count(out, "duplicate-token here"); n != 1 {
		t.Fatalf("cross-file duplicate not deduped (x%d): %q", n, out)
	}
}

// A broad pattern must stay under the hard output cap and say it stopped.
func TestContextSearchBoundsOutput(t *testing.T) {
	root := t.TempDir()
	var b strings.Builder
	for i := 0; i < 500; i++ {
		fmt.Fprintf(&b, "noisy hit %d %s\nfiller %d\n", i, strings.Repeat("x", 400), i)
	}
	writeArchive(t, root, "s1", "0001-a.md", b.String())

	out := execSearch(t, NewContextSearch(root), `{"pattern":"noisy hit"}`)
	if len(out) > contextSearchMaxOutput+300 {
		t.Fatalf("output %d bytes exceeds cap %d", len(out), contextSearchMaxOutput)
	}
	if !strings.Contains(out, "refine the pattern") {
		t.Fatalf("want truncation notice, got %q", out)
	}
	// Long lines are clamped, not dumped raw.
	for _, line := range strings.Split(out, "\n") {
		if len(line) > 4*contextSearchMaxLineRunes {
			t.Fatalf("unclamped line (%d bytes): %q", len(line), line)
		}
	}
}

// max_results and context_lines are honored and clamped to hard ceilings.
func TestContextSearchBudgetArgs(t *testing.T) {
	root := t.TempDir()
	var b strings.Builder
	for i := 0; i < 40; i++ {
		fmt.Fprintf(&b, "hit %d\nfiller\nfiller\nfiller\nfiller\n", i)
	}
	writeArchive(t, root, "s1", "0001-a.md", b.String())
	tool := NewContextSearch(root)

	out := execSearch(t, tool, `{"pattern":"hit ","max_results":2,"context_lines":0}`)
	if n := strings.Count(out, "hit "); n != 2 {
		t.Fatalf("max_results=2 gave %d snippets: %q", n, out)
	}

	// Absurd budgets are clamped, never honored as given.
	out = execSearch(t, tool, `{"pattern":"hit ","max_results":9999,"context_lines":9999}`)
	if n := strings.Count(out, ".md:"); n > contextSearchMaxResults {
		t.Fatalf("max_results not clamped: %d refs", n)
	}
	if len(out) > contextSearchMaxOutput+300 {
		t.Fatalf("clamped run still over cap: %d bytes", len(out))
	}
}

func TestContextSearchPatternRequired(t *testing.T) {
	tool := NewContextSearch(t.TempDir())
	for _, args := range []string{`{}`, `{"pattern":""}`, `{"pattern":[]}`, `{"pattern":["  "]}`} {
		if _, err := tool.Exec(context.Background(), json.RawMessage(args)); err == nil {
			t.Fatalf("want error for %s", args)
		}
	}
}

func TestContextSearchNoMatch(t *testing.T) {
	root := t.TempDir()
	writeArchive(t, root, "s1", "0001-a.md", "nothing relevant\n")
	out := execSearch(t, NewContextSearch(root), `{"pattern":"absent-token"}`)
	if !strings.Contains(out, "no matches") {
		t.Fatalf("got %q", out)
	}
}

func TestContextSearchPatternLimits(t *testing.T) {
	tool := NewContextSearch(t.TempDir())
	long := strings.Repeat("x", contextSearchMaxPatternRunes+1)
	if _, err := tool.Exec(context.Background(), json.RawMessage(fmt.Sprintf(`{"pattern":%q}`, long))); err == nil {
		t.Fatal("long pattern should be rejected")
	}
	patterns := make([]string, contextSearchMaxPatterns+1)
	for i := range patterns {
		patterns[i] = fmt.Sprintf("pattern-%d", i)
	}
	raw, err := json.Marshal(map[string]any{"pattern": patterns})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tool.Exec(context.Background(), raw); err == nil {
		t.Fatal("oversized pattern list should be rejected")
	}
}

func TestContextSearchCumulativeBudget(t *testing.T) {
	root := t.TempDir()
	writeArchive(t, root, "s1", "0001-a.md", "persistent-token "+strings.Repeat("x", 400)+"\n")
	tool := NewContextSearch(root)
	impl := tool.(*contextSearchTool)
	// The result is small for this query, so exercise enough calls to consume
	// the byte budget rather than assuming every call fills the 6k cap.
	for i := 0; i < 1_000; i++ {
		out := execSearch(t, tool, `{"pattern":"persistent-token","max_results":1,"context_lines":0}`)
		if strings.Contains(out, "retrieval budget exhausted") {
			return
		}
	}
	t.Fatalf("repeated searches bypassed cumulative retrieval budget (charged %d bytes)", impl.retrieved)
}

func TestContextSearchReadOnlyAndSessionless(t *testing.T) {
	if NewContextSearch("  ") != nil {
		t.Fatal("want nil tool when sessions are disabled")
	}
	ro, ok := NewContextSearch(t.TempDir()).(interface{ ReadOnly() bool })
	if !ok || !ro.ReadOnly() {
		t.Fatal("context_search must stay read-only")
	}
}
