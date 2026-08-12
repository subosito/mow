package review

import "testing"

func TestValidateRequestRejectsVerifierWithSkipVerification(t *testing.T) {
	err := ValidateRequest(Request{SkipVerification: true, Verifier: &fakeReviewer{}})
	if err == nil {
		t.Fatal("want error")
	}
}

func TestValidateRequestAllowsVerifierAlone(t *testing.T) {
	if err := ValidateRequest(Request{Verifier: &fakeReviewer{}}); err != nil {
		t.Fatal(err)
	}
}

func TestRunRejectsVerifierWithSkipVerification(t *testing.T) {
	sc := testScope(t)
	_, err := Run(t.Context(), &fakeReviewer{}, sc, Request{
		Profile:          GeneralProfile(),
		SkipVerification: true,
		Verifier:         &fakeReviewer{},
	})
	if err == nil {
		t.Fatal("want error")
	}
}
