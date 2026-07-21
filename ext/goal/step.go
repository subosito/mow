package goal

import "github.com/subosito/mow"

// StepOutcome is how one outer-loop step finished.
type StepOutcome string

const (
	// OutcomeDone — whole goal complete.
	OutcomeDone StepOutcome = "done"
	// OutcomeFailed — whole goal failed.
	OutcomeFailed StepOutcome = "failed"
	// OutcomeContinue — progress made; run another step.
	OutcomeContinue StepOutcome = "continue"
	// OutcomeRetry — soft failure (upstream blip); retry step budget.
	OutcomeRetry StepOutcome = "retry"
	// OutcomeBudget — hit user MaxTurns; soft-continue outer loop.
	OutcomeBudget StepOutcome = "budget"
)

// StepRequest is one unit of work for the Executor.
type StepRequest struct {
	State State
}

// StepResult is the structured result of one Executor step.
// The Runner only advances durable state from this (not free-form scraping).
type StepResult struct {
	Outcome   StepOutcome
	Summary   string
	Reason    string // failure reason when OutcomeFailed
	Text      string // raw assistant text
	SessionID string
	Usage     mow.Usage
	// Plan is the checklist after this step (may be updated by goal_report).
	Plan Plan
}
