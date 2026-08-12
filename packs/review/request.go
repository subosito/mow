package review

import "fmt"

// ValidateRequest checks programmatic Request invariants before a review run.
func ValidateRequest(req Request) error {
	if req.SkipVerification && req.Verifier != nil {
		return fmt.Errorf("review: Verifier cannot be set when SkipVerification is true")
	}
	return nil
}
