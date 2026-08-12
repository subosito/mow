package review

import "testing"

func TestApplyReviewerProvenanceExtrasIndependent(t *testing.T) {
	extra := map[string]string{}
	applyReviewerProvenanceExtras(extra, []string{"alpha", "beta"})
	if extra["reviewer_count"] != "2" || extra["reviewer_consensus"] != "independent" {
		t.Fatalf("extra = %+v", extra)
	}
	if extra["reviewers"] != "alpha, beta" {
		t.Fatalf("reviewers = %q", extra["reviewers"])
	}
}

func TestApplyReviewerProvenanceExtrasSingle(t *testing.T) {
	extra := map[string]string{}
	applyReviewerProvenanceExtras(extra, []string{"solo"})
	if extra["reviewer_count"] != "1" || extra["reviewer_consensus"] != "single" {
		t.Fatalf("extra = %+v", extra)
	}
	if _, ok := extra["reviewers"]; ok {
		t.Fatal("single reviewer should not set reviewers plural")
	}
}

func TestApplyVerifierAgreementIndependentConfirmed(t *testing.T) {
	f := Finding{Extra: map[string]string{"reviewer_count": "2", "reviewer_consensus": "independent"}}
	applyVerifierAgreement(&f, Verdict{Status: "confirmed"})
	if f.Extra["verifier_agreement"] != "confirmed_independent" {
		t.Fatalf("got %q", f.Extra["verifier_agreement"])
	}
}

func TestApplyVerifierAgreementSingleUncertain(t *testing.T) {
	f := Finding{Extra: map[string]string{"reviewer": "solo", "reviewer_count": "1"}}
	applyVerifierAgreement(&f, Verdict{Status: "uncertain"})
	if f.Extra["verifier_agreement"] != "uncertain" {
		t.Fatalf("got %q", f.Extra["verifier_agreement"])
	}
}
