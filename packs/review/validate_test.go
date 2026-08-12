package review

import (
	"strings"
	"testing"
)

func TestNormalizePath(t *testing.T) {
	const ws = "/home/dev/repo"
	tests := []struct {
		raw     string
		want    string
		wantErr bool
	}{
		{"internal/api/users.go", "internal/api/users.go", false},
		{"./internal/api/users.go", "internal/api/users.go", false},
		{"  internal/api/users.go  ", "internal/api/users.go", false},
		{"internal\\api\\users.go", "internal/api/users.go", false},
		{"internal/api/users.go:87", "internal/api/users.go", false},
		{"/home/dev/repo/internal/api/users.go", "internal/api/users.go", false},
		{"internal/./api/../api/users.go", "internal/api/users.go", false},
		{"../outside.go", "", true},
		{"/etc/passwd", "", true},
		{"/home/dev/repo", "", true},
		{"", "", true},
		{"   ", "", true},
	}
	for _, tt := range tests {
		got, err := NormalizePath(tt.raw, ws)
		if tt.wantErr {
			if err == nil {
				t.Errorf("NormalizePath(%q) = %q, want error", tt.raw, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("NormalizePath(%q): %v", tt.raw, err)
			continue
		}
		if got != tt.want {
			t.Errorf("NormalizePath(%q) = %q want %q", tt.raw, got, tt.want)
		}
	}
}

func TestNormalizeLines(t *testing.T) {
	tests := []struct {
		start, end, lines int
		wantS, wantE      int
		wantNote          bool
	}{
		{10, 20, 100, 10, 20, false},
		{20, 10, 100, 10, 20, false}, // swapped
		{0, 15, 100, 15, 0, false},   // end-only becomes start
		{-4, -2, 100, 0, 0, false},
		{10, 500, 100, 10, 100, true}, // clamped
		{500, 0, 100, 0, 0, true},     // beyond EOF
		{10, 20, 0, 10, 20, false},    // unknown file length
	}
	for _, tt := range tests {
		s, e, note := normalizeLines(tt.start, tt.end, tt.lines)
		if s != tt.wantS || e != tt.wantE || (note != "") != tt.wantNote {
			t.Errorf("normalizeLines(%d,%d,%d) = %d,%d,%q", tt.start, tt.end, tt.lines, s, e, note)
		}
	}
}

func validFinding() Finding {
	return Finding{
		Severity: SevHigh, Confidence: ConfMedium, Category: CatCorrectness,
		Title: "Possible nil dereference", Path: "internal/api/users.go",
		StartLine: 10, EndLine: 12, Evidence: "handler dereferences user before the nil check",
		Impact: "panic", Recommendation: "check for nil",
	}
}

func testOpts() ValidationOptions {
	return ValidationOptions{
		Profile:   GeneralProfile(),
		InScope:   func(rel string) bool { return strings.HasPrefix(rel, "internal/") },
		FileLines: func(rel string) (int, bool) { return 100, strings.HasSuffix(rel, ".go") },
	}
}

func TestValidateHappyPath(t *testing.T) {
	out, issues := Validate([]Finding{validFinding()}, "/ws", testOpts())
	if len(out) != 1 || len(issues) != 0 {
		t.Fatalf("out=%d issues=%v", len(out), issues)
	}
	f := out[0]
	if f.ID != "review-001" {
		t.Errorf("id = %q", f.ID)
	}
	if !strings.HasPrefix(f.Fingerprint, "sha256:") {
		t.Errorf("fingerprint = %q", f.Fingerprint)
	}
	if len(f.Locations) != 1 || f.Locations[0].Role != "primary" {
		t.Errorf("locations = %+v", f.Locations)
	}
}

func TestValidateRejectsBadFindings(t *testing.T) {
	mut := func(fn func(*Finding)) Finding {
		f := validFinding()
		fn(&f)
		return f
	}
	cases := []struct {
		name string
		f    Finding
		want string
	}{
		{"no title", mut(func(f *Finding) { f.Title = "" }), "title"},
		{"no evidence", mut(func(f *Finding) { f.Evidence = "  " }), "evidence"},
		{"no severity", mut(func(f *Finding) { f.Severity = SevUnknown }), "severity"},
		{"no confidence", mut(func(f *Finding) { f.Confidence = ConfUnknown }), "confidence"},
		{"escaping path", mut(func(f *Finding) { f.Path = "../../etc/passwd" }), "escapes"},
		{"missing file", mut(func(f *Finding) { f.Path = "internal/api/nope.txt" }), "does not exist"},
		{"out of scope", mut(func(f *Finding) { f.Path = "cmd/mow/main.go" }), "outside the reviewed scope"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, issues := Validate([]Finding{tc.f}, "/ws", testOpts())
			if len(out) != 0 {
				t.Fatalf("finding survived validation: %+v", out)
			}
			if len(issues) != 1 || !strings.Contains(issues[0].Reason, tc.want) {
				t.Fatalf("issues = %v, want reason containing %q", issues, tc.want)
			}
			if !issues[0].Dropped {
				t.Error("issue should be marked dropped")
			}
		})
	}
}

func TestValidateAllowOutOfScope(t *testing.T) {
	opt := testOpts()
	opt.AllowOutOfScope = true
	f := validFinding()
	f.Path = "cmd/mow/main.go"
	out, _ := Validate([]Finding{f}, "/ws", opt)
	if len(out) != 1 {
		t.Fatalf("want kept finding, got %d", len(out))
	}
	if !strings.Contains(out[0].VerificationNotes, "outside reviewed scope") {
		t.Errorf("missing out-of-scope note: %q", out[0].VerificationNotes)
	}
}

func TestValidateDedupesAndSorts(t *testing.T) {
	low := validFinding()
	low.Severity = SevLow
	low.Confidence = ConfLow
	dup := validFinding()
	dup.StartLine = 55 // same fingerprint (line drift), stronger severity
	other := validFinding()
	other.Title = "Unchecked error from Close"
	other.Severity = SevMedium

	out, issues := Validate([]Finding{low, dup, other}, "/ws", testOpts())
	if len(out) != 2 {
		t.Fatalf("want 2 findings after dedupe, got %d: %+v", len(out), out)
	}
	if out[0].Severity < out[1].Severity {
		t.Errorf("findings not sorted worst-first: %v %v", out[0].Severity, out[1].Severity)
	}
	if out[0].ID != "review-001" || out[1].ID != "review-002" {
		t.Errorf("ids not sequential after sort: %q %q", out[0].ID, out[1].ID)
	}
	if len(issues) != 1 || !strings.Contains(issues[0].Reason, "duplicate") {
		t.Fatalf("expected one duplicate issue, got %v", issues)
	}
	// Merged duplicate must carry the stronger severity/confidence.
	var merged Finding
	for _, f := range out {
		if f.Title == low.Title {
			merged = f
		}
	}
	if merged.Severity != SevHigh || merged.Confidence != ConfMedium {
		t.Errorf("merge did not keep the stronger values: %v/%v", merged.Severity, merged.Confidence)
	}
}

func TestValidateMergeReviewerProvenance(t *testing.T) {
	a := validFinding()
	a.Extra = map[string]string{"reviewer": "alpha"}
	b := validFinding()
	b.Extra = map[string]string{"reviewer": "beta"}
	out, _ := Validate([]Finding{a, b}, "/ws", testOpts())
	if len(out) != 1 {
		t.Fatalf("want one merged finding, got %d", len(out))
	}
	if out[0].Extra["reviewer"] != "alpha" {
		t.Fatalf("reviewer = %q", out[0].Extra["reviewer"])
	}
	if out[0].Extra["reviewers"] != "alpha, beta" {
		t.Fatalf("reviewers = %q", out[0].Extra["reviewers"])
	}
	if out[0].Extra["reviewer_count"] != "2" || out[0].Extra["reviewer_consensus"] != "independent" {
		t.Fatalf("consensus extras = %+v", out[0].Extra)
	}
}

func TestValidateSecurityProfileIDsAndExtras(t *testing.T) {
	f := validFinding()
	f.Category = "authorization"
	f.Extra = map[string]string{"Attack-Surface": "HTTP path parameter", "title": "nope", "blank": "  "}
	opt := testOpts()
	opt.Profile = SecurityProfile()
	out, _ := Validate([]Finding{f}, "/ws", opt)
	if len(out) != 1 {
		t.Fatalf("got %d findings", len(out))
	}
	if out[0].ID != "sec-001" {
		t.Errorf("id = %q, want sec-001", out[0].ID)
	}
	if out[0].Category != CatAuthz {
		t.Errorf("category = %q", out[0].Category)
	}
	if out[0].Extra["attack_surface"] != "HTTP path parameter" {
		t.Errorf("extra keys not canonicalized: %+v", out[0].Extra)
	}
	if _, ok := out[0].Extra["title"]; ok {
		t.Error("extra must not shadow a base field")
	}
	if _, ok := out[0].Extra["blank"]; ok {
		t.Error("blank extras should be dropped")
	}
}

func TestValidateSecurityEvidenceFieldsOptional(t *testing.T) {
	// Structured security evidence is optional: a finding with none still validates.
	f := validFinding()
	f.Category = "injection"
	opt := testOpts()
	opt.Profile = SecurityProfile()
	out, issues := Validate([]Finding{f}, "/ws", opt)
	if len(out) != 1 || len(issues) != 0 {
		t.Fatalf("optional evidence missing must not drop findings: out=%d issues=%v", len(out), issues)
	}

	// Full evidence set is preserved (keys canonicalized, values redacted).
	full := validFinding()
	full.Category = "sql-injection"
	full.Extra = map[string]string{
		"Source":                 "r.URL.Query().Get(\"q\")",
		"Sink":                   "db.Query(\"SELECT …\" + q)",
		"Sanitizers-Considered":  "none found on handler path",
		"Reachability":           "reachable for any HTTP client",
		"Attacker-Prerequisites": "network access to API",
		"Evidence-Limitations":   "WAF rules not inspected",
		"Attack-Surface":         "public HTTP",
		"Trust-Boundary":         "internet → SQL",
		"Exploitability":         "trivial with crafted q",
		"CWE":                    "CWE-89",
		"token":                  "sk-abcdefghijklmnopqrstuvwxyz", // secret-shaped value
	}
	out, _ = Validate([]Finding{full}, "/ws", opt)
	if len(out) != 1 {
		t.Fatalf("got %d findings", len(out))
	}
	ex := out[0].Extra
	for _, k := range SecurityEvidenceFields {
		if strings.TrimSpace(ex[k]) == "" {
			t.Errorf("missing or empty evidence field %q: %+v", k, ex)
		}
	}
	if !strings.Contains(ex["token"], "[redacted]") && ex["token"] == "sk-abcdefghijklmnopqrstuvwxyz" {
		t.Errorf("secret-shaped extra not redacted: %q", ex["token"])
	}
	// Absence of evidence fields must not break general profile either.
	gen := validFinding()
	gen.Extra = map[string]string{"source": "should still pass through"}
	gout, _ := Validate([]Finding{gen}, "/ws", testOpts())
	if gout[0].Extra["source"] != "should still pass through" {
		t.Errorf("unknown extras should pass through on general: %+v", gout[0].Extra)
	}
}

func TestValidateClampsLongText(t *testing.T) {
	f := validFinding()
	f.Evidence = strings.Repeat("x", maxTextField+500)
	out, _ := Validate([]Finding{f}, "/ws", testOpts())
	if len(out) != 1 {
		t.Fatal("finding dropped")
	}
	if len(out[0].Evidence) > maxTextField+32 || !strings.HasSuffix(out[0].Evidence, "(truncated)") {
		t.Errorf("evidence not clamped: len=%d", len(out[0].Evidence))
	}
}

func TestValidateNormalizesExtraLocations(t *testing.T) {
	f := validFinding()
	f.Locations = []Location{
		{Path: "internal/api/users.go", StartLine: 10}, // dup of primary
		{Path: "internal/db/query.go", StartLine: 4, Role: "Sink"},
		{Path: "../evil.go", StartLine: 1},
		{Path: "internal/missing.txt", StartLine: 1},
	}
	out, _ := Validate([]Finding{f}, "/ws", testOpts())
	locs := out[0].Locations
	if len(locs) != 2 {
		t.Fatalf("locations = %+v", locs)
	}
	if locs[0].Role != "primary" || locs[1].Role != "sink" || locs[1].Path != "internal/db/query.go" {
		t.Errorf("unexpected locations: %+v", locs)
	}
}
