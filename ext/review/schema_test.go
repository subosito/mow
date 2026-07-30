package review

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestParseSeverityAndConfidence(t *testing.T) {
	tests := []struct {
		in   string
		want Severity
		ok   bool
	}{
		{"critical", SevCritical, true},
		{" HIGH ", SevHigh, true},
		{"Major", SevHigh, true},
		{"moderate", SevMedium, true},
		{"minor", SevLow, true},
		{"nit", SevInfo, true},
		{"", SevUnknown, false},
		{"catastrophic", SevUnknown, false},
	}
	for _, tt := range tests {
		got, ok := ParseSeverity(tt.in)
		if got != tt.want || ok != tt.ok {
			t.Errorf("ParseSeverity(%q) = %v,%v want %v,%v", tt.in, got, ok, tt.want, tt.ok)
		}
	}
	if !SevHigh.Valid() || SevUnknown.Valid() {
		t.Error("Valid() disagrees with the unknown zero value")
	}
	if SevCritical <= SevHigh || SevHigh <= SevInfo {
		t.Error("severity ordering is not monotonic")
	}

	conf := []struct {
		in   string
		want Confidence
		ok   bool
	}{
		{"high", ConfHigh, true},
		{"Likely", ConfMedium, true},
		{"speculative", ConfLow, true},
		{"maybe-ish", ConfUnknown, false},
	}
	for _, tt := range conf {
		got, ok := ParseConfidence(tt.in)
		if got != tt.want || ok != tt.ok {
			t.Errorf("ParseConfidence(%q) = %v,%v want %v,%v", tt.in, got, ok, tt.want, tt.ok)
		}
	}
}

func TestEnumJSONRoundTrip(t *testing.T) {
	type payload struct {
		Sev  Severity   `json:"severity"`
		Conf Confidence `json:"confidence"`
	}
	b, err := json.Marshal(payload{SevHigh, ConfMedium})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if got := string(b); got != `{"severity":"high","confidence":"medium"}` {
		t.Fatalf("marshal = %s", got)
	}
	var back payload
	if err := json.Unmarshal([]byte(`{"severity":"WARNING","confidence":"certain"}`), &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if back.Sev != SevMedium || back.Conf != ConfHigh {
		t.Fatalf("round trip = %v/%v", back.Sev, back.Conf)
	}
	if err := json.Unmarshal([]byte(`{"severity":"nope"}`), &back); err == nil {
		t.Fatal("want error for unknown severity")
	}
	if _, err := json.Marshal(payload{}); err == nil {
		t.Fatal("want error marshaling unset enums")
	}
}

func TestNormalizeCategory(t *testing.T) {
	gen := GeneralProfile().Categories
	sec := SecurityProfile().Categories
	tests := []struct {
		raw     string
		allowed []Category
		want    Category
	}{
		{"correctness", gen, CatCorrectness},
		{"Error Handling", gen, CatErrorHandling},
		{"bug", gen, CatCorrectness},
		{"authz", gen, CatOther}, // security label in a general review
		{"authorization", sec, CatAuthz},
		{"sql-injection", sec, CatInjection},
		{"", gen, CatOther},
		{"whatever", sec, CatOther},
	}
	for _, tt := range tests {
		if got := NormalizeCategory(tt.raw, tt.allowed); got != tt.want {
			t.Errorf("NormalizeCategory(%q) = %q want %q", tt.raw, got, tt.want)
		}
	}
}

func TestFingerprintStability(t *testing.T) {
	a := Finding{Category: CatCorrectness, Path: "internal/api/users.go", Title: "Possible nil dereference when user lookup misses", StartLine: 87}
	b := a
	b.StartLine = 412 // line drift must not change identity
	b.Title = "possible nil dereference, when user lookup misses!"
	if Fingerprint("general", a) != Fingerprint("general", b) {
		t.Error("fingerprint changed on line drift / reworded title")
	}
	if Fingerprint("general", a) == Fingerprint("security", a) {
		t.Error("fingerprint must be profile-scoped")
	}
	c := a
	c.Path = "internal/api/other.go"
	if Fingerprint("general", a) == Fingerprint("general", c) {
		t.Error("fingerprint must depend on path")
	}
	if !strings.HasPrefix(Fingerprint("general", a), "sha256:") {
		t.Error("fingerprint should be prefixed with its hash algorithm")
	}
}

func TestReportRecountAndMaxSeverity(t *testing.T) {
	r := NewReport("general")
	if r.SchemaVersion != SchemaVersion || !r.Advisory {
		t.Fatal("NewReport must set schema version and advisory")
	}
	r.Findings = []Finding{
		{Severity: SevHigh}, {Severity: SevHigh}, {Severity: SevLow}, {Severity: SevInfo},
	}
	r.Recount()
	if r.Counts.High != 2 || r.Counts.Low != 1 || r.Counts.Info != 1 || r.Counts.Total != 4 {
		t.Fatalf("counts = %+v", r.Counts)
	}
	if r.MaxSeverity() != SevHigh {
		t.Fatalf("MaxSeverity = %v", r.MaxSeverity())
	}
	if NewReport("x").MaxSeverity() != SevUnknown {
		t.Fatal("empty report should have unknown max severity")
	}
}

func TestFindingJSONFlattensExtra(t *testing.T) {
	f := Finding{
		ID: "sec-001", Severity: SevHigh, Confidence: ConfMedium, Category: CatAuthz,
		Title: "Missing authorization check", Path: "internal/api/account.go", StartLine: 142,
		Evidence: "no ownership check",
		Extra:    map[string]string{"attack_surface": "HTTP path parameter", "title": "ignored"},
	}
	b, err := json.Marshal(f)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if m["attack_surface"] != "HTTP path parameter" {
		t.Errorf("extra field not flattened: %v", m)
	}
	if m["title"] != "Missing authorization check" {
		t.Errorf("extra key must not override a base field: %v", m["title"])
	}

	var back Finding
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatalf("round trip: %v", err)
	}
	if back.Extra["attack_surface"] != "HTTP path parameter" {
		t.Errorf("extra lost on decode: %+v", back.Extra)
	}
	if back.Severity != SevHigh || back.Title != f.Title {
		t.Errorf("base fields lost on decode: %+v", back)
	}
}

func TestFindingUnmarshalNonStringExtra(t *testing.T) {
	var f Finding
	raw := `{"title":"t","severity":"low","confidence":"low","path":"a.go","evidence":"e","cwe":89,"reachable":true,"ignored":null}`
	if err := json.Unmarshal([]byte(raw), &f); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if f.Extra["cwe"] != "89" || f.Extra["reachable"] != "true" {
		t.Errorf("scalar extras = %+v", f.Extra)
	}
	if _, ok := f.Extra["ignored"]; ok {
		t.Error("null extras should be dropped")
	}
}
