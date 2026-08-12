package review

import "testing"

func reportWithSeverities(profile string, sevs ...Severity) *Report {
	rep := NewReport(profile)
	for _, s := range sevs {
		rep.Findings = append(rep.Findings, Finding{Severity: s, Title: "t", Path: "a.go", Evidence: "e"})
	}
	return rep.Recount()
}

func truncatedReport(profile string) *Report {
	rep := NewReport(profile)
	rep.Run.Truncated = true
	rep.Run.TruncationReason = "file limit 15"
	rep.Scope.FilesReviewed = 15
	return rep
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
		{"truncated scope fails when configured", ExitPolicy{FailOnTruncated: true}, truncatedReport("general"), ExitFindings},
		{"truncated scope default passes", ExitPolicy{}, truncatedReport("general"), ExitClean},
		// --exit-zero is advisory: it wins over both findings and truncation.
		{"exit-zero overrides truncated", ExitPolicy{ExitZero: true, FailOnTruncated: true}, truncatedReport("general"), ExitClean},
		{"exit-zero overrides findings and truncated", ExitPolicy{ExitZero: true, FailOnTruncated: true}, func() *Report {
			rep := reportWithSeverities("general", SevCritical)
			rep.Run.Truncated = true
			return rep
		}(), ExitClean},
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

func TestExitInfoTruncatedScopeReason(t *testing.T) {
	info := (ExitPolicy{FailOnTruncated: true}).ExitInfo(truncatedReport("general"))
	if info.Code != ExitFindings || len(info.Reasons) != 1 || info.Reasons[0] != ExitReasonTruncatedScope {
		t.Fatalf("info = %+v", info)
	}
}

func TestExitInfoBothTruncationAndFindings(t *testing.T) {
	rep := truncatedReport("general")
	rep.Findings = []Finding{{Severity: SevHigh, Title: "t", Path: "a.go", Evidence: "e"}}
	rep = rep.Recount()
	info := (ExitPolicy{FailOnTruncated: true}).ExitInfo(rep)
	if info.Code != ExitFindings || len(info.Reasons) != 2 {
		t.Fatalf("info = %+v", info)
	}
	if info.Reasons[0] != ExitReasonTruncatedScope || info.Reasons[1] != ExitReasonFindingSeverity {
		t.Fatalf("reasons = %v", info.Reasons)
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
