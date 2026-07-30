package review

import (
	"errors"
	"strings"
	"testing"
)

func TestExtractJSONObject(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    string
		wantErr error
	}{
		{"bare object", `{"findings":[]}`, `{"findings":[]}`, nil},
		{"leading prose", "Here is my review:\n{\"findings\":[]}", `{"findings":[]}`, nil},
		{"trailing prose", "{\"findings\":[]}\nHope that helps!", `{"findings":[]}`, nil},
		{"fenced json", "```json\n{\"findings\":[]}\n```", `{"findings":[]}`, nil},
		{"fenced no tag", "```\n{\"findings\":[]}\n```", `{"findings":[]}`, nil},
		{"prose with braces then fence", "I considered {a} and {b}.\n```json\n{\"findings\":[1]}\n```", `{"findings":[1]}`, nil},
		{"nested braces", `{"a":{"b":{"c":1}}}`, `{"a":{"b":{"c":1}}}`, nil},
		{"brace inside string", `{"t":"has } brace","x":1}`, `{"t":"has } brace","x":1}`, nil},
		{"escaped quote in string", `{"t":"say \"} \" ok","x":1}`, `{"t":"say \"} \" ok","x":1}`, nil},
		{"empty", "   ", "", ErrNoJSON},
		{"no object", "I found no issues.", "", ErrNoJSON},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ExtractJSONObject(tt.in)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("err = %v want %v", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Errorf("got %q want %q", got, tt.want)
			}
		})
	}
}

func TestExtractJSONObjectUnterminated(t *testing.T) {
	_, err := ExtractJSONObject(`{"findings":[{"title":"x"`)
	if err == nil || !strings.Contains(err.Error(), "unterminated") {
		t.Fatalf("err = %v, want unterminated", err)
	}
}

func TestParseCandidates(t *testing.T) {
	reply := `Sure, here you go:
` + "```json" + `
{
  "findings": [
    {
      "title": "Possible nil dereference",
      "category": "correctness",
      "severity": "high",
      "confidence": "medium",
      "path": "internal/api/users.go",
      "start_line": 87,
      "end_line": 90,
      "evidence": "findUser can return nil, nil",
      "impact": "panic",
      "recommendation": "nil check",
      "affected_behavior": "user lookup"
    }
  ],
  "summary": "one high issue"
}
` + "```"
	env, err := parseCandidates(reply)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(env.Findings) != 1 {
		t.Fatalf("findings = %d", len(env.Findings))
	}
	f := env.Findings[0]
	if f.Severity != SevHigh || f.Confidence != ConfMedium {
		t.Errorf("enums = %v/%v", f.Severity, f.Confidence)
	}
	if f.StartLine != 87 || f.EndLine != 90 {
		t.Errorf("lines = %d-%d", f.StartLine, f.EndLine)
	}
	if f.Extra["affected_behavior"] != "user lookup" {
		t.Errorf("profile extra lost: %+v", f.Extra)
	}
	if env.Summary != "one high issue" {
		t.Errorf("summary = %q", env.Summary)
	}
}

func TestParseCandidatesRejectsBadContract(t *testing.T) {
	// A bad severity must fail loudly rather than silently yielding no findings:
	// "no findings" would be reported to the user as a clean review.
	if _, err := parseCandidates(`{"findings":[{"severity":"apocalyptic","title":"x"}]}`); err == nil {
		t.Fatal("want error for invalid severity")
	}
	if _, err := parseCandidates(`{"findings":"not an array"}`); err == nil {
		t.Fatal("want error for wrong type")
	}
	if _, err := parseCandidates("I did not find anything."); !errors.Is(err, ErrNoJSON) {
		t.Fatalf("err = %v want ErrNoJSON", err)
	}
}

func TestParseCandidatesEmptyFindingsIsValid(t *testing.T) {
	env, err := parseCandidates(`{"findings": [], "summary": "looks fine"}`)
	if err != nil {
		t.Fatalf("empty findings must be a valid answer: %v", err)
	}
	if len(env.Findings) != 0 || env.Summary != "looks fine" {
		t.Errorf("env = %+v", env)
	}
}

func TestParseVerdicts(t *testing.T) {
	env, err := parseVerdicts(`{"verdicts":[
	  {"id":"review-001","status":"confirmed","reason":"checked the caller"},
	  {"id":"review-002","status":"rejected","reason":"already guarded upstream"},
	  {"id":"review-003","status":"uncertain","severity":"low"}
	],"summary":"one survived"}`)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(env.Verdicts) != 3 {
		t.Fatalf("verdicts = %d", len(env.Verdicts))
	}
	if !env.Verdicts[0].Confirmed() || env.Verdicts[0].Rejected() {
		t.Error("verdict 1 should be confirmed")
	}
	if !env.Verdicts[1].Rejected() || env.Verdicts[1].Confirmed() {
		t.Error("verdict 2 should be rejected")
	}
	v3 := env.Verdicts[2]
	if v3.Confirmed() || v3.Rejected() {
		t.Error("uncertain is neither confirmed nor rejected")
	}
}

func TestVerdictStatusIsCaseInsensitive(t *testing.T) {
	if !(Verdict{Status: " CONFIRMED "}).Confirmed() {
		t.Error("status matching should tolerate case and spaces")
	}
	if !(Verdict{Status: "Rejected"}).Rejected() {
		t.Error("status matching should tolerate case")
	}
}
