package review

import (
	"strconv"
)

// applyReviewerProvenanceExtras stamps ensemble candidate provenance on a finding.
func applyReviewerProvenanceExtras(extra map[string]string, names []string) {
	if len(names) == 0 {
		return
	}
	if extra == nil {
		return
	}
	extra["reviewer"] = names[0]
	extra["reviewer_count"] = strconv.Itoa(len(names))
	if len(names) > 1 {
		extra["reviewers"] = joinReviewerNames(names)
		extra["reviewer_consensus"] = "independent"
	} else {
		delete(extra, "reviewers")
		extra["reviewer_consensus"] = "single"
	}
}

func joinReviewerNames(names []string) string {
	if len(names) == 0 {
		return ""
	}
	out := names[0]
	for i := 1; i < len(names); i++ {
		out += ", " + names[i]
	}
	return out
}

// applyVerifierAgreement records how pass two ruled relative to candidate
// provenance. Only called for findings that remain in the report (confirmed or
// uncertain); rejected candidates are dropped before this runs.
func applyVerifierAgreement(f *Finding, v Verdict) {
	if f.Extra == nil {
		f.Extra = map[string]string{}
	}
	independent := reviewerCountFromExtra(f.Extra) > 1
	switch {
	case v.Confirmed():
		if independent {
			f.Extra["verifier_agreement"] = "confirmed_independent"
		} else {
			f.Extra["verifier_agreement"] = "confirmed"
		}
	default:
		// Uncertain (and any non-confirmed, non-rejected status that survived).
		if independent {
			f.Extra["verifier_agreement"] = "uncertain_independent"
		} else {
			f.Extra["verifier_agreement"] = "uncertain"
		}
	}
}

func reviewerCountFromExtra(extra map[string]string) int {
	if extra == nil {
		return 0
	}
	if c := extra["reviewer_count"]; c != "" {
		if n, err := strconv.Atoi(c); err == nil && n > 0 {
			return n
		}
	}
	if rs := extra["reviewers"]; rs != "" {
		return len(reviewerNames(Finding{Extra: extra}))
	}
	if extra["reviewer"] != "" {
		return 1
	}
	return 0
}
