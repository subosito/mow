package review

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestRenderJSONRoundTrip(t *testing.T) {
	out := renderToString(t, sampleReport("security"), FormatJSON, TextOptions{})
	var back Report
	if err := json.Unmarshal([]byte(out), &back); err != nil {
		t.Fatalf("JSON output must round-trip: %v\n%s", err, out)
	}
	if back.SchemaVersion != SchemaVersion || !back.Advisory {
		t.Errorf("envelope invariants lost: %+v", back)
	}
	if back.Profile != "security" || len(back.Findings) != 2 {
		t.Errorf("report = %+v", back)
	}
	if back.Counts.High != 1 || back.Counts.Low != 1 || back.Counts.Total != 2 {
		t.Errorf("counts = %+v", back.Counts)
	}
	f := back.Findings[0]
	if f.Severity != SevHigh || f.Confidence != ConfHigh {
		t.Errorf("enums did not survive: %v/%v", f.Severity, f.Confidence)
	}
	if f.Extra["affected_behavior"] != "user lookup" {
		t.Errorf("profile extras lost: %+v", f.Extra)
	}
	if back.Suppressed != 3 {
		t.Errorf("suppressed = %d", back.Suppressed)
	}
}

func TestRenderJSONShape(t *testing.T) {
	out := renderToString(t, sampleReport("general"), FormatJSON, TextOptions{})
	var m map[string]any
	if err := json.Unmarshal([]byte(out), &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	// Keys the draft schema promises consumers.
	for _, k := range []string{"schema_version", "profile", "advisory", "run", "scope", "counts", "findings", "summary"} {
		if _, ok := m[k]; !ok {
			t.Errorf("missing top-level key %q", k)
		}
	}
	findings := m["findings"].([]any)
	first := findings[0].(map[string]any)
	for _, k := range []string{"id", "fingerprint", "severity", "confidence", "category", "title", "path", "evidence"} {
		if _, ok := first[k]; !ok {
			t.Errorf("finding missing key %q", k)
		}
	}
	// Extra fields must be flat, not nested under "extra".
	if _, nested := first["extra"]; nested {
		t.Error("profile extras should be flattened, not nested")
	}
	if first["affected_behavior"] != "user lookup" {
		t.Errorf("flattened extra missing: %v", first)
	}
}

func TestRenderJSONL(t *testing.T) {
	out := renderToString(t, sampleReport("general"), FormatJSONL, TextOptions{})
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 3 {
		t.Fatalf("want envelope + 2 findings, got %d lines", len(lines))
	}
	var head Report
	if err := json.Unmarshal([]byte(lines[0]), &head); err != nil {
		t.Fatalf("envelope line: %v", err)
	}
	if len(head.Findings) != 0 {
		t.Error("envelope line should carry no findings")
	}
	if head.Counts.Total != 2 || head.Scope.FilesReviewed != 8 {
		t.Errorf("envelope lost metadata: %+v", head)
	}
	for _, ln := range lines[1:] {
		var f Finding
		if err := json.Unmarshal([]byte(ln), &f); err != nil {
			t.Fatalf("finding line: %v", err)
		}
		if f.ID == "" || f.Title == "" {
			t.Errorf("finding line incomplete: %s", ln)
		}
	}
}

func TestRenderSARIF(t *testing.T) {
	out := renderToString(t, sampleReport("security"), FormatSARIF, TextOptions{})
	var log map[string]any
	if err := json.Unmarshal([]byte(out), &log); err != nil {
		t.Fatalf("SARIF must be valid JSON: %v", err)
	}
	if log["version"] != "2.1.0" {
		t.Errorf("version = %v", log["version"])
	}
	if _, ok := log["$schema"]; !ok {
		t.Error("missing $schema")
	}
	runs := log["runs"].([]any)
	if len(runs) != 1 {
		t.Fatalf("runs = %d", len(runs))
	}
	run := runs[0].(map[string]any)
	driver := run["tool"].(map[string]any)["driver"].(map[string]any)
	if driver["name"] != "mow sec" {
		t.Errorf("driver name = %v", driver["name"])
	}
	rules := driver["rules"].([]any)
	if len(rules) != 2 { // correctness + tests
		t.Fatalf("rules = %d: %v", len(rules), rules)
	}
	results := run["results"].([]any)
	if len(results) != 2 {
		t.Fatalf("results = %d", len(results))
	}
	r0 := results[0].(map[string]any)
	if r0["level"] != "error" {
		t.Errorf("high severity should map to SARIF error, got %v", r0["level"])
	}
	if !strings.HasPrefix(r0["ruleId"].(string), "mow/security/") {
		t.Errorf("ruleId should be profile-namespaced: %v", r0["ruleId"])
	}
	loc := r0["locations"].([]any)[0].(map[string]any)["physicalLocation"].(map[string]any)
	if loc["artifactLocation"].(map[string]any)["uri"] != "internal/api/users.go" {
		t.Errorf("bad artifact uri: %v", loc)
	}
	region := loc["region"].(map[string]any)
	if region["startLine"].(float64) != 87 || region["endLine"].(float64) != 90 {
		t.Errorf("region = %v", region)
	}
	if fp := r0["partialFingerprints"].(map[string]any); fp["mow/v1"] != "sha256:abc" {
		t.Errorf("fingerprint not exported: %v", fp)
	}
	// Related locations let a reviewer follow the data path.
	if _, ok := r0["relatedLocations"]; !ok {
		t.Error("secondary locations should be exported as relatedLocations")
	}
	// Advisory nature must survive into SARIF.
	inv := run["invocations"].([]any)[0].(map[string]any)
	notes := inv["toolExecutionNotifications"].([]any)
	if !strings.Contains(notes[0].(map[string]any)["message"].(map[string]any)["text"].(string), "advisory") {
		t.Errorf("SARIF should disclose the advisory nature: %v", notes)
	}
	// Unverified finding should say so in the message.
	r1 := results[1].(map[string]any)
	if !strings.Contains(r1["message"].(map[string]any)["text"].(string), "Not confirmed") {
		t.Errorf("unverified finding should be flagged: %v", r1["message"])
	}
}

func TestRenderSARIFTruncationWarning(t *testing.T) {
	rep := sampleReport("general")
	rep.Run.Truncated = true
	rep.Run.TruncationReason = "budget small: file limit reached"
	out := renderToString(t, rep, FormatSARIF, TextOptions{})
	if !strings.Contains(out, "scope was truncated") && !strings.Contains(out, "was truncated") {
		t.Errorf("truncated SARIF run must warn:\n%s", out)
	}
}

func TestSARIFOmitsRegionWithoutLine(t *testing.T) {
	rep := NewReport("general")
	rep.Findings = []Finding{{
		ID: "review-001", Severity: SevMedium, Confidence: ConfLow, Category: CatOther,
		Title: "No line known", Path: "a.go", Evidence: "e",
		Locations: []Location{{Path: "a.go", Role: "primary"}},
	}}
	rep.Recount()
	out := renderToString(t, rep, FormatSARIF, TextOptions{})
	var log sarifLog
	if err := json.Unmarshal([]byte(out), &log); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	loc := log.Runs[0].Results[0].Locations[0]
	if loc.PhysicalLocation.Region != nil {
		t.Error("no region should be emitted when the line is unknown")
	}
}

func TestSARIFLevelMapping(t *testing.T) {
	tests := []struct{ sev, want string }{
		{"critical", "error"}, {"high", "error"}, {"medium", "warning"},
		{"low", "note"}, {"info", "note"},
	}
	for _, tt := range tests {
		s, _ := ParseSeverity(tt.sev)
		if got := sarifLevel(s); got != tt.want {
			t.Errorf("sarifLevel(%s) = %q want %q", tt.sev, got, tt.want)
		}
	}
}

func TestParseFormat(t *testing.T) {
	for _, name := range append(FormatNames(), "") {
		if _, err := ParseFormat(name); err != nil {
			t.Errorf("ParseFormat(%q): %v", name, err)
		}
	}
	if f, _ := ParseFormat(""); f != FormatText {
		t.Errorf("empty format should default to text, got %q", f)
	}
	if f, _ := ParseFormat(" JSON "); f != FormatJSON {
		t.Errorf("format parsing should be lenient, got %q", f)
	}
	if _, err := ParseFormat("yaml"); err == nil {
		t.Error("want error for unknown format")
	}
}

func TestRenderNilReportAllFormats(t *testing.T) {
	for _, f := range []Format{FormatText, FormatJSON, FormatJSONL, FormatSARIF} {
		if err := Render(&strings.Builder{}, nil, f, TextOptions{}); err == nil {
			t.Errorf("format %s: want error for nil report", f)
		}
	}
}
