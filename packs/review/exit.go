package review

// Exit codes. These are the contract CI depends on, so they are defined once
// here rather than scattered through the CLI.
const (
	// ExitClean means the review ran and nothing met the fail-on threshold.
	ExitClean = 0
	// ExitFindings means the review ran and found something at or above the
	// fail-on severity. This is a successful run with actionable output — not
	// a tool error.
	ExitFindings = 1
	// ExitError means the review could not be completed or trusted: bad flags,
	// git/scope failure, model error, or output that failed the schema
	// contract. Never conflate this with "clean".
	ExitError = 2
)

// ExitPolicy decides the process exit code for a finished report.
type ExitPolicy struct {
	// FailOn is the lowest severity that makes the command fail. Unset uses
	// the profile default.
	FailOn Severity
	// ExitZero forces ExitClean for any successful run (advisory CI jobs that
	// must never block a merge).
	ExitZero bool
}

// ExitCode maps a completed report to a process exit code. A nil report is an
// error: no report means nothing was verified, which must not read as clean.
func (p ExitPolicy) ExitCode(rep *Report) int {
	if rep == nil {
		return ExitError
	}
	if p.ExitZero {
		return ExitClean
	}
	threshold := p.FailOn
	if !threshold.Valid() {
		if prof, ok := LookupProfile(rep.Profile); ok {
			threshold = prof.FailOn
		} else {
			threshold = SevHigh
		}
	}
	if rep.MaxSeverity() >= threshold {
		return ExitFindings
	}
	return ExitClean
}
