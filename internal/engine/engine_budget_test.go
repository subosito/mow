package engine

import (
	"strings"
	"testing"

	"github.com/subosito/mow/internal/agent"
	"github.com/subosito/mow/internal/config"
)

// budgetLimits must pair the configured ceiling with catalog pricing, since a
// USD limit is only enforceable when both directions are priced.
func TestEngineBudgetLimits(t *testing.T) {
	t.Parallel()

	t.Run("unset means no gate", func(t *testing.T) {
		t.Parallel()
		e := &Engine{cfg: &config.File{}}
		if e.budgetLimits().Set() {
			t.Error("no configured limit should yield no ceiling")
		}
		g, err := e.budgetGate()
		if err != nil || g != nil {
			t.Errorf("want no gate: (%v, %v)", g, err)
		}
	})

	t.Run("token limit builds without pricing", func(t *testing.T) {
		t.Parallel()
		cfg := &config.File{}
		cfg.Policy.MaxRunTokens = 50_000
		e := &Engine{cfg: cfg}
		g, err := e.budgetGate()
		if err != nil {
			t.Fatalf("token ceiling must not need prices: %v", err)
		}
		if g == nil {
			t.Fatal("want a gate")
		}
	})

	t.Run("usd limit on an unpriced model is an error", func(t *testing.T) {
		t.Parallel()
		cfg := &config.File{}
		cfg.Policy.MaxRunUSD = 5
		e := &Engine{cfg: cfg}
		_, err := e.budgetGate()
		if err == nil {
			t.Fatal("an unenforceable USD ceiling must be refused, not ignored")
		}
		if !strings.Contains(err.Error(), "max_run_tokens") {
			t.Errorf("error should name the workable alternative: %v", err)
		}
	})

	t.Run("usd limit with configured pricing builds", func(t *testing.T) {
		t.Parallel()
		cfg := &config.File{}
		cfg.Policy.MaxRunUSD = 5
		cfg.LLM.InputPrice = 3
		cfg.LLM.OutputPrice = 15
		e := &Engine{cfg: cfg}
		lim := e.budgetLimits()
		if !lim.Prices.Known() {
			t.Fatalf("configured prices should reach the gate: %+v", lim.Prices)
		}
		g, err := e.budgetGate()
		if err != nil || g == nil {
			t.Fatalf("want a gate: (%v, %v)", g, err)
		}
	})
}

// StopBudget must be distinct: a caller that cannot tell budget exhaustion
// from completion will report a half-finished job as done.
func TestBudgetStopReason(t *testing.T) {
	t.Parallel()
	if got := stopReasonFrom(agent.ErrBudget); got != StopBudget {
		t.Errorf("stopReasonFrom(ErrBudget) = %q, want %q", got, StopBudget)
	}
	if StopBudget == StopCompleted {
		t.Fatal("budget exhaustion must not look like completion")
	}
}
