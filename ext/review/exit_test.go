package review

import "testing"

func reportWithSeverities(profile string, sevs ...Severity) *Report {
	rep := NewReport(profile)
	for _, s := range sevs {
		rep.Findings = append(rep.Findings, Finding{Severity: s, Title: "t", Path: "a.go", Evidence: "e"})
	}
	return rep.Recount()
}

func TestExitCodeThresholds(t *testing.T) {
	tests := []struct {
		name   string
		policy ExitPolicy
		rep    *Report
		want   int
	}{
		{"clean report", ExitPolicy{}, reportWithSeverities("general"), ExitClean},
		{"below default fail-on", ExitPolicy{}, reportWithSeverities("general", SevMedium, SevLow), ExitClean},
		{"at default fail-on", ExitPolicy{}, reportWithSeverities("general", SevHigh), ExitFindings},
		{"above default fail-on", ExitPolicy{}, reportWithSeverities("general", SevCritical), ExitFindings},
		{"explicit fail-on medium", ExitPolicy{FailOn: SevMedium}, reportWithSeverities("general", SevMedium), ExitFindings},
		{"explicit fail-on critical", ExitPolicy{FailOn: SevCritical}, reportWithSeverities("general", SevHigh), ExitClean},
		{"exit-zero overrides findings", ExitPolicy{ExitZero: true}, reportWithSeverities("general", SevCritical), ExitClean},
		{"security profile default", ExitPolicy{}, reportWithSeverities("security", SevHigh), ExitFindings},
		{"unknown profile falls back to high", ExitPolicy{}, reportWithSeverities("mystery", SevHigh), ExitFindings},
		{"nil report is an error", ExitPolicy{}, nil, ExitError},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.policy.ExitCode(tt.rep); got != tt.want {
				t.Errorf("ExitCode = %d want %d", got, tt.want)
			}
		})
	}
}

// A nil report must never be reported as clean: no report means nothing was
// verified, which is a tooling failure, not a passing review.
func TestExitCodeNilReportIgnoresExitZero(t *testing.T) {
	if got := (ExitPolicy{ExitZero: true}).ExitCode(nil); got != ExitError {
		t.Errorf("nil report with --exit-zero = %d, want ExitError", got)
	}
}

func TestExitCodeConstantsAreDistinct(t *testing.T) {
	if ExitClean == ExitFindings || ExitFindings == ExitError || ExitClean == ExitError {
		t.Fatal("exit codes must be distinct")
	}
	if ExitClean != 0 {
		t.Error("clean must be 0 for shell conventions")
	}
}

func TestProfileFailOnDefaults(t *testing.T) {
	// Both profiles fail on high by default; security reports from medium up.
	if GeneralProfile().FailOn != SevHigh || SecurityProfile().FailOn != SevHigh {
		t.Error("default fail-on should be high for both profiles")
	}
	if SecurityProfile().MinSeverity <= GeneralProfile().MinSeverity {
		t.Error("security should report a narrower, higher-signal set by default")
	}
}
