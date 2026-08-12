package review

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestApplyVerdictEvidenceCorrections(t *testing.T) {
	prof := SecurityProfile()
	f := Finding{
		ID:    "sec-001",
		Title: "Path traversal",
		Extra: map[string]string{
			"source":       "HTTP path",
			"sink":         "os.Open",
			"reachability": "public route",
		},
	}
	v := Verdict{
		ID:     "sec-001",
		Status: "confirmed",
		EvidenceFields: map[string]json.RawMessage{
			"reachability": json.RawMessage(`"conditional — auth required"`),
			"sink":         json.RawMessage(`null`),
		},
	}
	notes, err := applyVerdictEvidenceCorrections(&f, v, prof)
	if err != nil {
		t.Fatal(err)
	}
	if f.Extra["reachability"] != "conditional — auth required" {
		t.Fatalf("reachability = %q", f.Extra["reachability"])
	}
	if _, ok := f.Extra["sink"]; ok {
		t.Fatal("sink should be cleared")
	}
	if !strings.Contains(strings.Join(notes, " "), "corrected") || !strings.Contains(strings.Join(notes, " "), "cleared") {
		t.Fatalf("notes = %v", notes)
	}
}

func TestApplyVerdictEvidenceCorrectionsIgnoresGeneralReview(t *testing.T) {
	f := Finding{Extra: map[string]string{"source": "x"}}
	v := Verdict{EvidenceFields: map[string]json.RawMessage{"source": json.RawMessage(`"y"`)}}
	if _, err := applyVerdictEvidenceCorrections(&f, v, GeneralProfile()); err != nil {
		t.Fatal(err)
	}
	if f.Extra["source"] != "x" {
		t.Fatal("general review must ignore evidence_fields")
	}
}

func TestApplyVerdictEvidenceCorrectionsRejectsUnknownField(t *testing.T) {
	f := Finding{ID: "sec-001", Title: "t", Extra: map[string]string{}}
	v := Verdict{EvidenceFields: map[string]json.RawMessage{"made_up": json.RawMessage(`"x"`)}}
	notes, err := applyVerdictEvidenceCorrections(&f, v, SecurityProfile())
	if err != nil {
		t.Fatalf("unknown keys should not abort: %v", err)
	}
	if len(notes) != 1 || !strings.Contains(notes[0], "ignored unknown evidence field") {
		t.Fatalf("notes = %v", notes)
	}
}

func TestApplyVerdictEvidenceCorrectionsRedactsSecrets(t *testing.T) {
	f := Finding{ID: "sec-001", Title: "t", Extra: map[string]string{}}
	v := Verdict{EvidenceFields: map[string]json.RawMessage{
		"source": json.RawMessage(`"api_key=supersecretvalue12345"`),
	}}
	if _, err := applyVerdictEvidenceCorrections(&f, v, SecurityProfile()); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(f.Extra["source"], "supersecretvalue12345") {
		t.Fatalf("secret leaked: %q", f.Extra["source"])
	}
}

func TestApplyVerdictEvidenceCorrectionsNotesAreDeterministic(t *testing.T) {
	f := Finding{
		ID: "sec-001", Title: "t",
		Extra: map[string]string{"source": "old-src", "sink": "old-sink", "reachability": "old"},
	}
	v := Verdict{EvidenceFields: map[string]json.RawMessage{
		"sink":         json.RawMessage(`null`),
		"source":       json.RawMessage(`"new-src"`),
		"reachability": json.RawMessage(`"reachable"`),
	}}
	notes, err := applyVerdictEvidenceCorrections(&f, v, SecurityProfile())
	if err != nil {
		t.Fatal(err)
	}
	// Sorted by raw key: reachability, sink, source.
	want := []string{
		`pass 2 corrected "reachability" on "t"`,
		`pass 2 cleared "sink" on "t"`,
		`pass 2 corrected "source" on "t"`,
	}
	if strings.Join(notes, "\n") != strings.Join(want, "\n") {
		t.Fatalf("notes =\n%s\nwant\n%s", strings.Join(notes, "\n"), strings.Join(want, "\n"))
	}
}

func TestApplyVerdictEvidenceCorrectionsRejectsEmptyKey(t *testing.T) {
	f := Finding{ID: "sec-001", Title: "t", Extra: map[string]string{}}
	v := Verdict{EvidenceFields: map[string]json.RawMessage{"  ": json.RawMessage(`"x"`)}}
	if _, err := applyVerdictEvidenceCorrections(&f, v, SecurityProfile()); err == nil || !strings.Contains(err.Error(), "empty evidence field key") {
		t.Fatalf("err = %v", err)
	}
}
