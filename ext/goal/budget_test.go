package goal

import (
	"strings"
	"testing"
	"time"
)

func TestHardBudgetExceededTokens(t *testing.T) {
	hit, why := hardBudgetExceeded(State{MaxInputTokens: 100, InputTokens: 100})
	if !hit || why == "" {
		t.Fatalf("hit=%v why=%q", hit, why)
	}
	hit, _ = hardBudgetExceeded(State{MaxInputTokens: 100, InputTokens: 50})
	if hit {
		t.Fatal("should not hit")
	}
}

func TestHardBudgetExceededDuration(t *testing.T) {
	st := State{
		MaxDurationMs: int64((time.Millisecond * 5).Milliseconds()),
		StartedAt:     time.Now().UTC().Add(-time.Second),
	}
	hit, why := hardBudgetExceeded(st)
	if !hit || !strings.Contains(why, "wall-clock") {
		t.Fatalf("hit=%v why=%q", hit, why)
	}
}

func TestNormalizeSpecBudgets(t *testing.T) {
	s, err := NormalizeSpec(Spec{Goal: "x", MaxInputTokens: -3, MaxDuration: -time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if s.MaxInputTokens != 0 || s.MaxDuration != 0 {
		t.Fatalf("%+v", s)
	}
}
