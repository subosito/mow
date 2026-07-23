package mow

import "testing"

func TestLookupModelLimits(t *testing.T) {
	cases := map[string]int{
		"claude-sonnet-5":   200_000,
		"gpt-4.1-mini":      1_000_000,
		"gpt-4.1":           1_000_000,
		"gpt-4o-mini":       128_000,
		"deepseek-v4-flash": 128_000,
		"totally-unknown":   0,
	}
	for model, wantCtx := range cases {
		if got := lookupModelLimits(model).ContextWindow; got != wantCtx {
			t.Errorf("%s ctx=%d want %d", model, got, wantCtx)
		}
	}
	// mini must not match the broader gpt-4.1 row's price.
	if lookupModelLimits("gpt-4.1-mini").InputPrice != 0.4 {
		t.Fatalf("gpt-4.1-mini price=%v", lookupModelLimits("gpt-4.1-mini").InputPrice)
	}
}

func TestUsageCost(t *testing.T) {
	u := Usage{InputTokens: 1_000_000, OutputTokens: 500_000}
	l := ModelLimits{InputPrice: 3, OutputPrice: 15}
	// 1M*$3 + 0.5M*$15 = 3 + 7.5 = 10.5
	if got := u.Cost(l); got != 10.5 {
		t.Fatalf("cost=%v want 10.5", got)
	}
	if got := u.Cost(ModelLimits{}); got != 0 {
		t.Fatalf("unknown price cost=%v want 0", got)
	}
}
