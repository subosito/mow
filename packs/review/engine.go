package review

import (
	"context"
	"errors"
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
	// Turn exhaustion arrives as an error, and it is the single most likely
	// way a review fails on a real repo: the pass spent its budget exploring
	// and never emitted its JSON. Say so in the user's terms — the generic
	// "max turns exceeded" points at --max-turns, but the review surface is
	// --budget, and the fix is usually a narrower scope.
	if errors.Is(err, mow.ErrAgentMaxTurns) {
		return "", fmt.Errorf("the review pass ran out of turns before reporting; " +
			"narrow the scope, or raise the budget with --budget large / --max-turns")
	}
	if err != nil {
		return "", err
	}
	// Belt and braces: if a future wire reports exhaustion as a stop reason
	// rather than an error, an empty reply must still not read as "no findings".
	if res.StopReason == "max_turns" && strings.TrimSpace(res.Text) == "" {
		return "", fmt.Errorf("the review pass ran out of turns before reporting; " +
			"narrow the scope or raise the budget (--budget large)")
	}
	return res.Text, nil
}

func (r *engineReviewer) Model() string { return r.eng.Model() }
