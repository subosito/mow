package review

import (
	"context"
	"fmt"
	"strings"

	"github.com/subosito/mow"
)

// engineReviewer adapts *mow.Engine to Reviewer.
//
// Each pass is an Ephemeral + ReadOnly prompt: ephemeral so pass 1's raw JSON
// never becomes context for pass 2 (the verifier must re-derive evidence from
// code, not inherit the candidate reasoning), and read-only so a review can
// never edit the code it is reviewing even if the engine was built with
// write/shell enabled.
type engineReviewer struct {
	eng *mow.Engine
}

var _ Reviewer = (*engineReviewer)(nil)

// NewEngineReviewer wraps an engine as a Reviewer.
func NewEngineReviewer(eng *mow.Engine) Reviewer { return &engineReviewer{eng: eng} }

func (r *engineReviewer) Ask(ctx context.Context, system, prompt string) (string, error) {
	res, err := r.eng.PromptWith(ctx, prompt, mow.PromptOpts{
		SystemAppend: system,
		ReadOnly:     true,
		Ephemeral:    true,
	})
	if err != nil {
		return "", err
	}
	// A pass that ran out of turns has usually not emitted its JSON yet.
	// Surfacing this as an error beats parsing a half-finished reply and
	// reporting the leftovers as if they were a complete review.
	if res.StopReason == "max_turns" && strings.TrimSpace(res.Text) == "" {
		return "", fmt.Errorf("review pass hit the turn limit before producing a report (raise --budget or narrow the scope)")
	}
	return res.Text, nil
}

func (r *engineReviewer) Model() string { return r.eng.Model() }
