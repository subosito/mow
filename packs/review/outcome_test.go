package review

import (
	"strings"
	"testing"
)

// "0 finding(s)" is the same message whether the review was genuinely clean,
// whether verification rejected everything, or whether --min-severity filtered
// it all out. With --format sarif the operator never sees the report body, so
// that one line is the only signal — and it currently reads as "you are fine"
// in all three cases.
func TestOutcomeSummaryDistinguishesCleanFromSuppressed(t *testing.T) {
	tests := []struct {
		name    string
		rep     *Report
		want    string
		wantNot string
	}{
		{
			name: "genuinely clean",
			rep: &Report{
				Counts: Counts{Total: 0},
				Scope:  ScopeInfo{FilesReviewed: 12},
			},
			want: "nothing reported by either pass",
		},
		{
			name: "everything suppressed",
			rep: &Report{
				Counts:     Counts{Total: 0},
				Suppressed: 7,
				Scope:      ScopeInfo{FilesReviewed: 150},
			},
			want: "7 suppressed",
			// Must not claim a clean read when candidates were dropped.
			wantNot: "nothing reported by either pass",
		},
		{
			name: "findings plus suppressed",
			rep: &Report{
				Counts:     Counts{Total: 3},
				Suppressed: 2,
				Scope:      ScopeInfo{FilesReviewed: 40},
			},
			want: "3 finding(s), 2 suppressed",
		},
		{
			name: "empty scope is not a clean read",
			rep: &Report{
				Counts: Counts{Total: 0},
				Scope:  ScopeInfo{FilesReviewed: 0},
			},
			wantNot: "nothing reported by either pass",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := outcomeSummary(tt.rep)
			if tt.want != "" && !strings.Contains(got, tt.want) {
				t.Errorf("outcomeSummary() = %q, want it to contain %q", got, tt.want)
			}
			if tt.wantNot != "" && strings.Contains(got, tt.wantNot) {
				t.Errorf("outcomeSummary() = %q, must not contain %q", got, tt.wantNot)
			}
		})
	}
}

func TestOutcomeSummaryNilReport(t *testing.T) {
	// A nil report is not a clean review; it is the absence of one.
	got := outcomeSummary(nil)
	if strings.Contains(got, "0 finding") {
		t.Errorf("nil report summarized as a finding count: %q", got)
	}
}
