package agent

import (
	"context"
	"math"
	"strings"
	"testing"

	"github.com/subosito/mow/internal/llm"
)

func TestRatioCalibratorSeedAndFallback(t *testing.T) {
	c := newRatioCalibrator()
	if got := c.Ratio(); got != defaultCharsPerToken {
		t.Fatalf("seed ratio=%v want %v", got, defaultCharsPerToken)
	}
	// Zero / negative usage must not move the estimate (provider omitted usage).
	c.Observe(40_000, 0)
	c.Observe(0, 10_000)
	c.Observe(-5, -5)
	if got := c.Ratio(); got != defaultCharsPerToken {
		t.Fatalf("ratio moved on bad samples: %v", got)
	}
	if c.samples != 0 {
		t.Fatalf("samples=%d want 0", c.samples)
	}
	var nilCal *ratioCalibrator
	if got := nilCal.Ratio(); got != defaultCharsPerToken {
		t.Fatalf("nil calibrator ratio=%v", got)
	}
	nilCal.Observe(10, 10) // must not panic
}

func TestRatioCalibratorConverges(t *testing.T) {
	tests := []struct {
		name          string
		chars, tokens int
		want          float64
	}{
		// Dense code / JSON tool output: ~2.5 chars per token.
		{"code heavy", 25_000, 10_000, 2.5},
		// English prose: ~5.5 chars per token.
		{"prose heavy", 55_000, 10_000, 5.5},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := newRatioCalibrator()
			for i := 0; i < 40; i++ {
				c.Observe(tc.chars, tc.tokens)
			}
			if math.Abs(c.Ratio()-tc.want) > 0.05 {
				t.Fatalf("ratio=%v want ~%v", c.Ratio(), tc.want)
			}
			if c.samples != 40 {
				t.Fatalf("samples=%d want 40", c.samples)
			}
		})
	}
}

func TestRatioCalibratorClamps(t *testing.T) {
	// Absurdly dense sample (1 char/token) clamps at the floor.
	low := newRatioCalibrator()
	for i := 0; i < 50; i++ {
		low.Observe(1_000, 1_000)
	}
	if got := low.Ratio(); got < minCharsPerToken-1e-9 || got > minCharsPerToken+0.01 {
		t.Fatalf("low ratio=%v want ~%v", got, minCharsPerToken)
	}
	// Absurdly sparse sample (40 chars/token) clamps at the ceiling.
	high := newRatioCalibrator()
	for i := 0; i < 50; i++ {
		high.Observe(40_000, 1_000)
	}
	if got := high.Ratio(); got > maxCharsPerToken+1e-9 || got < maxCharsPerToken-0.01 {
		t.Fatalf("high ratio=%v want ~%v", got, maxCharsPerToken)
	}
}

func TestRatioCalibratorSmooths(t *testing.T) {
	c := newRatioCalibrator()
	c.Observe(2_500, 1_000) // sample 2.5, seed 4 → 4 + 0.3*(2.5-4) = 3.55
	if got := c.Ratio(); math.Abs(got-3.55) > 1e-9 {
		t.Fatalf("after one sample ratio=%v want 3.55", got)
	}
	// A single outlier must not swing the estimate to the sample value.
	c2 := newRatioCalibrator()
	c2.Observe(8_000, 1_000) // clamped sample 8 → 4 + 0.3*(8-4) = 5.2
	if got := c2.Ratio(); math.Abs(got-5.2) > 1e-9 {
		t.Fatalf("outlier ratio=%v want 5.2", got)
	}
}

func TestBudgetCharsAndCompactTarget(t *testing.T) {
	// At the seed ratio budget chars == raw chars.
	if got := budgetChars(1_000, defaultCharsPerToken); got != 1_000 {
		t.Fatalf("budgetChars at seed=%d want 1000", got)
	}
	// Code-heavy (2 chars/token) doubles the effective cost.
	if got := budgetChars(1_000, 2); got != 2_000 {
		t.Fatalf("budgetChars code=%d want 2000", got)
	}
	// Prose-heavy (8 chars/token) halves it.
	if got := budgetChars(1_000, 8); got != 500 {
		t.Fatalf("budgetChars prose=%d want 500", got)
	}
	if got := budgetChars(0, 3); got != 0 {
		t.Fatalf("budgetChars(0)=%d", got)
	}
	// compactTarget is the inverse: budget 1000 at 2 chars/token → trim to 500 raw.
	if got := compactTarget(1_000, 2); got != 500 {
		t.Fatalf("compactTarget=%d want 500", got)
	}
	if got := compactTarget(0, 2); got != 0 {
		t.Fatalf("compactTarget(0)=%d", got)
	}
	if got := compactTarget(1_000, 0); got != 1_000 {
		t.Fatalf("compactTarget zero ratio=%d want 1000", got)
	}
}

// A calibrated code-heavy ratio must trigger compaction on a history that the
// fixed 4 chars/token heuristic would have let through untouched.
func TestApplyCompactUsesCalibratedRatio(t *testing.T) {
	msgs := []llm.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "refactor the compaction budget in internal/agent"},
	}
	for i := 0; i < 12; i++ {
		msgs = append(msgs,
			llm.Message{Role: "assistant", Content: strings.Repeat("a", 500)},
			llm.Message{Role: "tool", Name: "read", Content: strings.Repeat("x", 500)},
		)
	}
	raw := estChars(msgs)
	// Budget sits just above raw size: the fixed heuristic never compacts.
	opt := Options{MaxContextChars: raw + 1_000, MaxToolResultChars: 100_000}

	seed := newRatioCalibrator()
	out, err := applyCompact(context.Background(), msgs, opt, seed)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != len(msgs) {
		t.Fatalf("seed ratio compacted (%d → %d) but should not have", len(msgs), len(out))
	}

	// Now the provider reports a dense code ratio (~2 chars/token): the same
	// history costs ~2x the budget chars, so compaction must fire.
	dense := newRatioCalibrator()
	for i := 0; i < 40; i++ {
		dense.Observe(2_000, 1_000)
	}
	out2, err := applyCompact(context.Background(), msgs, opt, dense)
	if err != nil {
		t.Fatal(err)
	}
	if len(out2) >= len(msgs) {
		t.Fatalf("calibrated ratio did not compact: %d → %d", len(msgs), len(out2))
	}
	if estChars(out2) >= raw {
		t.Fatalf("compacted history not smaller: %d >= %d", estChars(out2), raw)
	}
}

// The PreCompact hook sees the calibrated ratio and the rescaled estimate.
func TestApplyCompactHookSeesRatio(t *testing.T) {
	msgs := []llm.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: strings.Repeat("q", 4_000)},
		{Role: "assistant", Content: strings.Repeat("a", 4_000)},
		{Role: "user", Content: strings.Repeat("z", 4_000)},
	}
	var gotRatio float64
	var gotEst, gotMax int
	opt := Options{
		MaxContextChars: 1_000,
		Hooks: Hooks{PreCompact: []PreCompactFunc{
			func(_ context.Context, e PreCompactEvent) (PreCompactDecision, error) {
				gotRatio, gotEst, gotMax = e.CharsPerToken, e.EstChars, e.MaxChars
				return PreCompactDecision{Skip: true}, nil
			},
		}},
	}
	c := newRatioCalibrator()
	for i := 0; i < 40; i++ {
		c.Observe(2_000, 1_000) // ~2 chars/token
	}
	if _, err := applyCompact(context.Background(), msgs, opt, c); err != nil {
		t.Fatal(err)
	}
	if math.Abs(gotRatio-2.0) > 0.05 {
		t.Fatalf("hook ratio=%v want ~2", gotRatio)
	}
	if want := budgetChars(estChars(msgs), gotRatio); gotEst != want {
		t.Fatalf("hook est=%d want %d", gotEst, want)
	}
	if gotMax != 1_000 {
		t.Fatalf("hook max=%d want 1000", gotMax)
	}
}
