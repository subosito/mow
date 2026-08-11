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

// Cache WRITES are a surcharge, not a discount, and on a long session they can
// dominate the bill. Measured on a real 406-turn opus session: 83M read tokens
// cost ~$125 while 33M write tokens cost ~$612. Pricing writes as plain input
// understated that session's largest line item.
func TestCostPricesCacheWritesAsASurcharge(t *testing.T) {
	t.Parallel()
	// Opus-tier list: $15 in, $75 out, $1.50 cache read (0.1x), $18.75 write (1.25x).
	lim := engine.ModelLimits{
		InputPrice: 15, OutputPrice: 75,
		CacheReadPrice: 1.50, CacheWritePrice: 18.75,
	}

	t.Run("writes cost more than plain input", func(t *testing.T) {
		t.Parallel()
		write := engine.Usage{InputTokens: 1_000_000, CacheWriteInputTokens: 1_000_000}
		fresh := engine.Usage{InputTokens: 1_000_000}
		cw, cf := write.Cost(lim), fresh.Cost(lim)
		if cw <= cf {
			t.Errorf("write cost %v should exceed fresh input %v", cw, cf)
		}
		if cw != 18.75 {
			t.Errorf("Cost = %v, want 18.75", cw)
		}
	})

	t.Run("read and write are disjoint subsets of input", func(t *testing.T) {
		t.Parallel()
		// 600k read + 300k write + 100k fresh = 0.9 + 5.625 + 1.5 = 8.025
		u := engine.Usage{
			InputTokens:           1_000_000,
			CachedInputTokens:     600_000,
			CacheWriteInputTokens: 300_000,
		}
		want := 8.025
		if got := u.Cost(lim); got < want-1e-9 || got > want+1e-9 {
			t.Errorf("Cost = %v, want %v", got, want)
		}
	})

	t.Run("over-reported shares cannot go negative", func(t *testing.T) {
		t.Parallel()
		u := engine.Usage{InputTokens: 100, CachedInputTokens: 90, CacheWriteInputTokens: 90}
		if got := u.Cost(lim); got < 0 {
			t.Errorf("Cost = %v, must not be negative", got)
		}
	})

	t.Run("unknown write rate falls back to input price", func(t *testing.T) {
		t.Parallel()
		// Understates (writes really cost more), but it is the only option
		// without a published rate.
		noRate := engine.ModelLimits{InputPrice: 15, OutputPrice: 75}
		u := engine.Usage{InputTokens: 1_000_000, CacheWriteInputTokens: 1_000_000}
		if got := u.Cost(noRate); got != 15.0 {
			t.Errorf("Cost = %v, want 15.0", got)
		}
	})
}

// Regression pinned to the observed session: write amplification, not reads,
// was the dominant cost. If a future change folds writes back into plain
// input, this catches it.
func TestRealSessionWriteAmplification(t *testing.T) {
	t.Parallel()
	lim := engine.ModelLimits{
		InputPrice: 15, OutputPrice: 75,
		CacheReadPrice: 1.50, CacheWritePrice: 18.75,
	}
	// Totals from a real 406-turn session.
	u := engine.Usage{
		InputTokens:           810 + 83_009_555 + 32_640_112,
		CachedInputTokens:     83_009_555,
		CacheWriteInputTokens: 32_640_112,
		OutputTokens:          257_198,
	}
	total := u.Cost(lim)
	writeOnly := engine.Usage{
		InputTokens:           32_640_112,
		CacheWriteInputTokens: 32_640_112,
	}.Cost(lim)

	if share := writeOnly / total; share < 0.7 {
		t.Errorf("cache writes were %.0f%% of cost, expected >70%%", share*100)
	}
	// Pricing writes as plain input hides the surcharge.
	naive := engine.Usage{
		InputTokens:       u.InputTokens,
		CachedInputTokens: u.CachedInputTokens,
		OutputTokens:      u.OutputTokens,
	}.Cost(lim)
	if naive >= total {
		t.Errorf("write-blind cost %v should be below write-aware %v", naive, total)
	}
	t.Logf("write-aware=$%.2f write-blind=$%.2f (understated by $%.2f); writes are %.0f%% of the bill",
		total, naive, total-naive, 100*writeOnly/total)
}
