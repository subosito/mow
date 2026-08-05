package review

import (
	"strings"
	"testing"
)

// sampleReport builds a representative two-finding report.
func sampleReport(profile string) *Report {
	rep := NewReport(profile)
	rep.Run = RunInfo{
		Tool: "mow " + map[string]string{"security": "sec"}[profile], Model: "test-model",
		Commit: "abc1234", Branch: "feature/x",
	}
	if profile != "security" {
		rep.Run.Tool = "mow review"
	}
	rep.Scope = ScopeInfo{
		Diff: "main...HEAD", FilesReviewed: 8, FilesExcluded: 2,
		Budget: "medium", Excluded: []string{"vendor/x.go", "go.sum"},
	}
	rep.Findings = []Finding{
		{
			ID: "review-001", Fingerprint: "sha256:abc", Severity: SevHigh, Confidence: ConfHigh,
			Category: CatCorrectness, Title: "Possible nil dereference when user lookup misses",
			Path: "internal/api/users.go", StartLine: 87, EndLine: 90,
			Locations: []Location{
				{Path: "internal/api/users.go", StartLine: 87, EndLine: 90, Role: "primary"},
				{Path: "internal/db/find.go", StartLine: 12, Role: "sink"},
			},
			Evidence:       "findUser can return nil, nil when the row is missing, but the handler dereferences user.ID before checking user == nil.",
			Impact:         "A missing user may panic the handler instead of returning 404.",
			Recommendation: "Check user == nil before accessing fields.",
			Verified:       true,
			Extra:          map[string]string{"affected_behavior": "user lookup"},
		},
		{
			ID: "review-002", Fingerprint: "sha256:def", Severity: SevLow, Confidence: ConfLow,
			Category: CatTests, Title: "No test covers the new error branch",
			Path: "internal/api/users.go", StartLine: 120,
			Locations: []Location{{Path: "internal/api/users.go", StartLine: 120, Role: "primary"}},
			Evidence:  "The added error path has no corresponding test case.",
			Verified:  false,
		},
	}
	rep.Suppressed = 3
	rep.Notes = []string{"pass 2 rejected \"Unchecked error\": guarded upstream"}
	rep.Recount()
	rep.Summary = "2 finding(s): 1 high, 1 low. 3 candidate(s) suppressed or rejected."
	return rep
}

func renderToString(t *testing.T, rep *Report, f Format, opt TextOptions) string {
	t.Helper()
	var b strings.Builder
	if err := Render(&b, rep, f, opt); err != nil {
		t.Fatalf("render %s: %v", f, err)
	}
	return b.String()
}

func TestRenderTextStructure(t *testing.T) {
	out := renderToString(t, sampleReport("general"), FormatText, TextOptions{})
	for _, want := range []string{
		"AI-assisted code review. Findings are advisory.",
		"Scope:",
		"profile:         general",
		"selection:       main...HEAD",
		"commit:          abc1234 (feature/x)",
		"files reviewed:  8",
		"files excluded:  2",
		"truncated:       false",
		"[HIGH] Possible nil dereference when user lookup misses",
		"id:             review-001",
		"path:           internal/api/users.go:87-90",
		"confidence:     high",
		"category:       correctness",
		"evidence:",
		"impact:",
		"recommendation:",
		"affected behavior:",
		"related:",
		"internal/db/find.go:12 (sink)",
		"[LOW] No test covers the new error branch",
		"verified:       no",
		"suppressed",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("text output missing %q\n---\n%s", want, out)
		}
	}
	// Worst-first ordering must survive rendering.
	if strings.Index(out, "[HIGH]") > strings.Index(out, "[LOW]") {
		t.Error("findings should render worst-first")
	}
	// No ANSI unless asked.
	if strings.Contains(out, "\x1b[") {
		t.Error("color must be opt-in")
	}
}

func TestRenderTextColorAndVerbose(t *testing.T) {
	out := renderToString(t, sampleReport("general"), FormatText, TextOptions{Color: true, Verbose: true})
	if !strings.Contains(out, "\x1b[1;31m") {
		t.Error("high severity should be colored when Color is set")
	}
	for _, want := range []string{"Excluded files:", "vendor/x.go", "Notes:", "rejected"} {
		if !strings.Contains(out, want) {
			t.Errorf("verbose output missing %q", want)
		}
	}
}

func TestRenderTextEmptyIsNotACleanBill(t *testing.T) {
	rep := NewReport("security")
	rep.Run.Tool = "mow sec"
	rep.Summary = "No findings at or above the reporting threshold."
	out := renderToString(t, rep, FormatText, TextOptions{})
	if !strings.Contains(out, "No findings.") {
		t.Error("empty report should say so")
	}
	if !strings.Contains(out, "not proof that the code is secure") {
		t.Errorf("empty security report must not read as proof of security:\n%s", out)
	}

	gen := NewReport("general")
	gen.Summary = "none"
	genOut := renderToString(t, gen, FormatText, TextOptions{})
	if !strings.Contains(genOut, "not proof that the code is correct") {
		t.Errorf("empty general report needs its own caveat:\n%s", genOut)
	}
}

func TestRenderTextTruncationIsVisible(t *testing.T) {
	rep := sampleReport("general")
	rep.Run.Truncated = true
	rep.Run.TruncationReason = "budget small: file limit 15 reached"
	out := renderToString(t, rep, FormatText, TextOptions{})
	if !strings.Contains(out, "truncated:       true") || !strings.Contains(out, "file limit 15") {
		t.Errorf("truncation must be visible in the scope header:\n%s", out)
	}
}

func TestRenderTextWrapsLongProse(t *testing.T) {
	rep := NewReport("general")
	rep.Findings = []Finding{{
		ID: "review-001", Severity: SevMedium, Confidence: ConfLow, Category: CatOther,
		Title: "Long", Path: "a.go", StartLine: 1,
		Evidence: strings.Repeat("word ", 100),
	}}
	rep.Recount()
	out := renderToString(t, rep, FormatText, TextOptions{})
	for _, line := range strings.Split(out, "\n") {
		if len(line) > 100 {
			t.Errorf("line too long (%d): %q", len(line), line)
		}
	}
}

func TestRenderTextNilReport(t *testing.T) {
	if err := RenderText(&strings.Builder{}, nil, TextOptions{}); err == nil {
		t.Fatal("want error for nil report")
	}
}

func TestFormatLocationRendering(t *testing.T) {
	tests := []struct {
		path       string
		start, end int
		want       string
	}{
		{"a.go", 10, 20, "a.go:10-20"},
		{"a.go", 10, 0, "a.go:10"},
		{"a.go", 10, 10, "a.go:10"},
		{"a.go", 0, 0, "a.go"},
	}
	for _, tt := range tests {
		if got := formatLocation(tt.path, tt.start, tt.end); got != tt.want {
			t.Errorf("formatLocation(%q,%d,%d) = %q want %q", tt.path, tt.start, tt.end, got, tt.want)
		}
	}
}
