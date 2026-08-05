package review

import (
	"strings"
	"testing"
)

func TestRedactSecrets(t *testing.T) {
	tests := []struct {
		name     string
		in       string
		mustHide string
		mustKeep string
	}{
		{
			name:     "openai style token",
			in:       "hardcoded key sk-abcdefghijklmnopqrstuvwx in config",
			mustHide: "sk-abcdefghijklmnopqrstuvwx",
			mustKeep: "hardcoded key",
		},
		{
			name:     "github token",
			in:       "found ghp_0123456789abcdefghijABCDEFGHIJ committed",
			mustHide: "ghp_0123456789abcdefghijABCDEFGHIJ",
			mustKeep: "committed",
		},
		{
			name:     "aws access key",
			in:       "AKIAIOSFODNN7EXAMPLE is checked in",
			mustHide: "AKIAIOSFODNN7EXAMPLE",
			mustKeep: "is checked in",
		},
		{
			name:     "assignment",
			in:       `password = "hunter2000secret"`,
			mustHide: "hunter2000secret",
			mustKeep: "password",
		},
		{
			name:     "jwt",
			in:       "token eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.dBjftJeZ4CVPmB92K here",
			mustHide: "dBjftJeZ4CVPmB92K",
			mustKeep: "here",
		},
		{
			name:     "url basic auth",
			in:       "dsn postgres://appuser:sup3rs3cret@db.internal:5432/app",
			mustHide: "sup3rs3cret",
			mustKeep: "appuser",
		},
		{
			name:     "pem private key",
			in:       "-----BEGIN RSA PRIVATE KEY-----\nMIIEpAIBAAKCAQEA\n-----END RSA PRIVATE KEY-----",
			mustHide: "MIIEpAIBAAKCAQEA",
			mustKeep: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := redactSecrets(tt.in)
			if strings.Contains(got, tt.mustHide) {
				t.Errorf("secret leaked: %q", got)
			}
			if !strings.Contains(got, redactionMark) {
				t.Errorf("no redaction marker: %q", got)
			}
			if tt.mustKeep != "" && !strings.Contains(got, tt.mustKeep) {
				t.Errorf("context lost, want %q in %q", tt.mustKeep, got)
			}
		})
	}
}

func TestRedactLeavesCleanTextAlone(t *testing.T) {
	clean := "findUser can return nil, nil when the row is missing; the handler dereferences user.ID."
	if got := redactSecrets(clean); got != clean {
		t.Errorf("clean prose was modified:\n got %q\nwant %q", got, clean)
	}
	if redactSecrets("") != "" {
		t.Error("empty string should stay empty")
	}
}

func TestValidateRedactsFindings(t *testing.T) {
	f := validFinding()
	f.Evidence = "the literal ghp_0123456789abcdefghijABCDEFGHIJ is committed"
	out, _ := Validate([]Finding{f}, "/ws", testOpts())
	if len(out) != 1 {
		t.Fatal("finding dropped")
	}
	if strings.Contains(out[0].Evidence, "ghp_0123456789") {
		t.Errorf("Validate must redact secrets: %q", out[0].Evidence)
	}
}

func TestRedactReportCoversSummaryAndNotes(t *testing.T) {
	r := NewReport("security")
	r.Summary = "leaked AKIAIOSFODNN7EXAMPLE in config"
	r.Notes = []string{"see password = correcthorsebattery"}
	r.Findings = []Finding{{Title: "t", Evidence: "sk-abcdefghijklmnopqrstuvwx"}}
	RedactReport(r)
	if strings.Contains(r.Summary, "AKIAIOSFODNN7EXAMPLE") {
		t.Errorf("summary not redacted: %q", r.Summary)
	}
	if strings.Contains(r.Notes[0], "correcthorsebattery") {
		t.Errorf("notes not redacted: %q", r.Notes[0])
	}
	if strings.Contains(r.Findings[0].Evidence, "sk-abcdefghij") {
		t.Errorf("finding not redacted: %q", r.Findings[0].Evidence)
	}
	if RedactReport(nil) != nil {
		t.Error("nil report should stay nil")
	}
}
