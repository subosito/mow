package engine_test

import (
	"testing"

	"github.com/subosito/mow/internal/engine"
)

// Cost must price cached input separately.
//
// An agent loop re-sends a large stable prefix every turn, so on a caching
// provider most "input" is billed at roughly a tenth of the headline rate.
// Charging it all at InputPrice overstated spend badly on long sessions and
// made the max_run_usd ceiling trip early — a ceiling that fires before the
// operator's real limit is as wrong as one that never fires.
func TestCostPricesCachedInputSeparately(t *testing.T) {
	t.Parallel()
	// $3/MTok in, $15/MTok out, $0.30/MTok cached (10% — a typical discount).
	lim := engine.ModelLimits{InputPrice: 3, OutputPrice: 15, CacheReadPrice: 0.30}

	t.Run("no cache reported prices everything as fresh", func(t *testing.T) {
		t.Parallel()
		u := engine.Usage{InputTokens: 1_000_000, OutputTokens: 0}
		if got := u.Cost(lim); got != 3.0 {
			t.Errorf("Cost = %v, want 3.0", got)
		}
	})

	t.Run("fully cached input uses the cache rate", func(t *testing.T) {
		t.Parallel()
		u := engine.Usage{InputTokens: 1_000_000, CachedInputTokens: 1_000_000}
		if got := u.Cost(lim); got != 0.30 {
			t.Errorf("Cost = %v, want 0.30 (all cached)", got)
		}
	})

	t.Run("mixed input splits at the boundary", func(t *testing.T) {
		t.Parallel()
		// 900k cached + 100k fresh = 0.27 + 0.30 = 0.57
		u := engine.Usage{InputTokens: 1_000_000, CachedInputTokens: 900_000}
		want := 0.57
		if got := u.Cost(lim); got < want-1e-9 || got > want+1e-9 {
			t.Errorf("Cost = %v, want %v", got, want)
		}
	})

	t.Run("output is unaffected", func(t *testing.T) {
		t.Parallel()
		u := engine.Usage{InputTokens: 0, OutputTokens: 1_000_000}
		if got := u.Cost(lim); got != 15.0 {
			t.Errorf("Cost = %v, want 15.0", got)
		}
	})

	t.Run("unknown cache rate falls back to input price", func(t *testing.T) {
		t.Parallel()
		// Conservative direction: may overstate, never understates.
		noCacheRate := engine.ModelLimits{InputPrice: 3, OutputPrice: 15}
		u := engine.Usage{InputTokens: 1_000_000, CachedInputTokens: 1_000_000}
		if got := u.Cost(noCacheRate); got != 3.0 {
			t.Errorf("Cost = %v, want 3.0 (fallback to input rate)", got)
		}
	})

	t.Run("cached exceeding total cannot go negative", func(t *testing.T) {
		t.Parallel()
		// Some gateways report a cached count above the total. Clamp rather
		// than emit a negative cost.
		u := engine.Usage{InputTokens: 100, CachedInputTokens: 500}
		if got := u.Cost(lim); got < 0 {
			t.Errorf("Cost = %v, must not be negative", got)
		}
	})

	t.Run("unknown prices still cost nothing", func(t *testing.T) {
		t.Parallel()
		u := engine.Usage{InputTokens: 1_000_000, CachedInputTokens: 500_000}
		if got := u.Cost(engine.ModelLimits{}); got != 0 {
			t.Errorf("Cost = %v, want 0 when prices are unknown", got)
		}
	})
}

// The real-world shape: a long session on a caching provider. The gap between
// naive and cache-aware accounting is the whole reason for this change.
func TestCostGapOnACachedSession(t *testing.T) {
	t.Parallel()
	lim := engine.ModelLimits{InputPrice: 3, OutputPrice: 15, CacheReadPrice: 0.30}
	// 30 turns re-sending a ~50k prefix, 90% served from cache.
	u := engine.Usage{InputTokens: 1_500_000, CachedInputTokens: 1_350_000, OutputTokens: 30_000}

	aware := u.Cost(lim)
	naive := engine.Usage{InputTokens: u.InputTokens, OutputTokens: u.OutputTokens}.Cost(lim)
	if aware >= naive {
		t.Fatalf("cache-aware cost %v should be below naive %v", aware, naive)
	}
	ratio := naive / aware
	if ratio < 2 {
		t.Errorf("expected a large gap on a 90%%-cached session, got %.2fx (%v vs %v)",
			ratio, naive, aware)
	}
	t.Logf("naive=$%.2f cache-aware=$%.2f (%.1fx overstatement)", naive, aware, ratio)
}
