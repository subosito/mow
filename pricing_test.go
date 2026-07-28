package mow

import "testing"

func TestLookupModelLimits(t *testing.T) {
	cases := map[string]int{
		"claude-sonnet-4": 200_000,
		"gpt-5-mini":      400_000,
		"gpt-5":           400_000,
		"gpt-5.4-mini":    400_000,
		"gpt-5.4":         1_000_000,
		"deepseek-chat":   128_000,
		"gemini-2.5-flash": 1_000_000,
		"totally-unknown": 0,
	}
	for model, wantCtx := range cases {
		if got := lookupModelLimits(model).ContextWindow; got != wantCtx {
			t.Errorf("%s ctx=%d want %d", model, got, wantCtx)
		}
	}
	// mini must not match the broader gpt-5 / gpt-5.4 row's price.
	if lookupModelLimits("gpt-5-mini").InputPrice != 0.25 {
		t.Fatalf("gpt-5-mini price=%v", lookupModelLimits("gpt-5-mini").InputPrice)
	}
	if lookupModelLimits("gpt-5.4-mini").InputPrice != 0.25 {
		t.Fatalf("gpt-5.4-mini price=%v", lookupModelLimits("gpt-5.4-mini").InputPrice)
	}
	if lookupModelLimits("gpt-5.4").ContextWindow != 1_000_000 {
		t.Fatalf("gpt-5.4 ctx=%d", lookupModelLimits("gpt-5.4").ContextWindow)
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
