package review

import "testing"

func TestSecurityEvidenceLevel(t *testing.T) {
	tests := []struct {
		name  string
		extra map[string]string
		want  string
	}{
		{"none", nil, "suspected"},
		{"partial flow", map[string]string{"source": "request query"}, "code-supported"},
		{"unknown reachability", map[string]string{"source": "request", "sink": "exec", "reachability": "unknown"}, "code-supported"},
		{"limited", map[string]string{"source": "request", "sink": "exec", "reachability": "reachable", "evidence_limitations": "framework config unavailable"}, "code-supported"},
		{"complete static evidence", map[string]string{"source": "request", "sink": "exec", "reachability": "reachable"}, "model-verified"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := securityEvidenceLevel(Finding{Extra: tt.extra}); got != tt.want {
				t.Fatalf("level=%q want %q", got, tt.want)
			}
		})
	}
}

func TestApplySecurityEvidenceLevelDoesNotAffectGeneral(t *testing.T) {
	findings := []Finding{{Extra: map[string]string{"source": "input", "sink": "exec"}}}
	applySecurityEvidenceLevel("general", findings)
	if _, ok := findings[0].Extra["evidence_level"]; ok {
		t.Fatal("general review must not gain security evidence metadata")
	}
}
