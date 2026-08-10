package agent

import (
	"context"
	"fmt"
	"strings"
)

// Spend ceiling.
//
// Until now the only bound on a runaway run was MaxTurns, which is a proxy for
// cost rather than a measure of it: 120 turns on a small context is pennies,
// 120 turns on a 500k-token history is real money. This is the measure.
//
// Design notes worth keeping:
//
//   - The check is admission control, not a tripwire. It refuses a call whose
//     *projected* total would exceed the limit, rather than noticing after the
//     fact — one turn can carry a huge history, so post-hoc detection can
//     overshoot by more than the entire budget.
//   - It over-estimates on purpose. Cost() ignores cache discounts and the
//     reply is priced at its maximum allowance, so the guard trips early
//     rather than late. That is the safe direction for a ceiling and the
//     wrong direction for a bill: the numbers reported here are a conservative
//     projection, never "what you were charged".
//   - Scope is one Run. A session-cumulative ceiling would need usage persisted
//     across resumes, which the session store does not record today; promising
//     a session-wide guarantee we cannot enforce would be worse than offering
//     none. Layering it later is additive: the effective remaining budget
//     becomes min(session remaining, run remaining).

// TokenPrices is USD per 1e6 tokens. Zero means the gateway did not publish a
// price for that direction.
type TokenPrices struct {
	InputPerMTok  float64
	OutputPerMTok float64
}

// Known reports whether both directions are priced. A USD ceiling is only
// meaningful when they are.
func (p TokenPrices) Known() bool {
	return p.InputPerMTok > 0 && p.OutputPerMTok > 0
}

// BudgetLimits configures the spend ceiling. Zero fields are unset.
type BudgetLimits struct {
	// MaxTokens caps InputTokens+OutputTokens for one Run. This is the honest
	// primitive: it works on every gateway, priced or not.
	MaxTokens int
	// MaxUSD caps projected cost for one Run. Requires published pricing;
	// NewBudgetGate rejects the configuration when prices are unknown, because
	// a ceiling that silently never fires is worse than no ceiling at all.
	MaxUSD float64
	// Prices are the per-MTok rates used for MaxUSD.
	Prices TokenPrices
}

// Set reports whether any limit is configured.
func (b BudgetLimits) Set() bool { return b.MaxTokens > 0 || b.MaxUSD > 0 }

// NewBudgetGate returns a PreModel hook enforcing b, or nil when no limit is
// set. It errors when MaxUSD is requested without usable prices.
func NewBudgetGate(b BudgetLimits) (PreModelFunc, error) {
	if !b.Set() {
		return nil, nil
	}
	if b.MaxUSD > 0 && !b.Prices.Known() {
		// Fail loudly at construction. Someone who writes max_run_usd is
		// stating a safety requirement, not asking for telemetry — silently
		// degrading to "no limit" would hand them a false guarantee.
		return nil, fmt.Errorf(
			"agent: max_run_usd set but the gateway publishes no price for this model; use max_run_tokens instead")
	}
	return func(ctx context.Context, e PreModelEvent) (PreModelDecision, error) {
		if reason := b.exceeded(e); reason != "" {
			return PreModelDecision{Stop: true, Reason: reason}, nil
		}
		return PreModelDecision{}, nil
	}, nil
}

// exceeded returns a human-readable reason when the projected total for the
// call described by e would breach a limit, or "" to allow it.
func (b BudgetLimits) exceeded(e PreModelEvent) string {
	inTok, outTok := projectCall(e)
	// Provider-reported consumption so far, plus this call's projection.
	usedIn := e.Usage.InputTokens + inTok
	usedOut := e.Usage.OutputTokens + outTok

	if b.MaxTokens > 0 {
		total := usedIn + usedOut
		if total > b.MaxTokens {
			return fmt.Sprintf(
				"token budget: %s consumed, this call projected at ~%s more, limit %s (turn %d)",
				thousands(e.Usage.InputTokens+e.Usage.OutputTokens),
				thousands(inTok+outTok), thousands(b.MaxTokens), e.Turn)
		}
	}
	if b.MaxUSD > 0 {
		spent := float64(e.Usage.InputTokens)/1e6*b.Prices.InputPerMTok +
			float64(e.Usage.OutputTokens)/1e6*b.Prices.OutputPerMTok
		next := float64(inTok)/1e6*b.Prices.InputPerMTok +
			float64(outTok)/1e6*b.Prices.OutputPerMTok
		if spent+next > b.MaxUSD {
			return fmt.Sprintf(
				"cost budget: ~$%.2f consumed, this call projected at ~$%.2f more, limit $%.2f "+
					"(turn %d; conservative projection, cache discounts not applied — not your actual bill)",
				spent, next, b.MaxUSD, e.Turn)
		}
	}
	return ""
}

// projectCall estimates the tokens this call will add.
//
// Input comes from the char estimate the loop already computed, divided by the
// calibrated chars/token ratio — the same number compaction budgets against.
// Output is charged at its full allowance rather than a historical average: a
// ceiling needs a conservative bound, and "the last few replies were short" is
// no guarantee about the next one.
func projectCall(e PreModelEvent) (inTok, outTok int) {
	cpt := e.CharsPerToken
	if cpt <= 0 {
		cpt = DefaultCharsPerToken
	}
	if e.SentChars > 0 {
		inTok = int(float64(e.SentChars) / cpt)
	}
	outTok = e.MaxOutputTokens
	if outTok <= 0 {
		// No configured reply cap: the provider may return anything. Assume a
		// substantial reply so the ceiling stays conservative rather than
		// pretending output is free.
		outTok = unknownOutputTokens
	}
	return inTok, outTok
}

// unknownOutputTokens is the assumed reply size when llm.max_tokens is unset.
// Chosen to be large enough that the guard does not under-count a long answer,
// small enough that it does not refuse the very first call on a modest budget.
const unknownOutputTokens = 8_000

// thousands formats n with underscore separators (12_345), matching how the
// limits are written in config.
func thousands(n int) string {
	s := fmt.Sprintf("%d", n)
	if len(s) <= 3 {
		return s
	}
	var b strings.Builder
	lead := len(s) % 3
	if lead > 0 {
		b.WriteString(s[:lead])
	}
	for i := lead; i < len(s); i += 3 {
		if b.Len() > 0 {
			b.WriteByte('_')
		}
		b.WriteString(s[i : i+3])
	}
	return b.String()
}
