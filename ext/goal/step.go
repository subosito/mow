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
	// OutcomeReroute — next step should change approach (different profile /
	// toolset / focus). Code-owned: returned by a Router, or by a stuck guard.
	OutcomeReroute StepOutcome = "reroute"
	// OutcomeRetrySame — retry this exact step (with validation feedback).
	// Code-owned; bounded by State.RetryCount and maxStepRetries.
	OutcomeRetrySame StepOutcome = "retry_same"
	// OutcomeEscalate — pause the goal for a human decision. The run stops
	// with StatusBlocked and State.Question; resume answers it (milestone 4).
	OutcomeEscalate StepOutcome = "escalate"
	// OutcomePartialStop — stop now with a partial result (budget, retry cap,
	// or code edge). StatusPartial + PartialSummaryFor.
	OutcomePartialStop StepOutcome = "partial_stop"
)

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
	// Evidence is durable facts recorded this step (goal_report evidence=...).
	Evidence []Fact
}
