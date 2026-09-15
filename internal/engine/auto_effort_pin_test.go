package engine

import (
	"testing"

	"github.com/subosito/mow/internal/llm"
)

// The downshift must only touch a tier mow chose on the user's behalf.
//
// EffortPinned is the existing "explicit operator choice" marker: SetEffort
// (/effort, llm.effort, MOW_EFFORT) and session resume set it, and it survives
// lean model picks when the new model allows the tier. So a catalog default
// of "high" is fair game, but a user who typed /effort high is not.
func TestAutoEffortRespectsExplicitPin(t *testing.T) {
	cases := []struct {
		name   string
		pinned bool
		want   string // runEffort after applyAutoEffort ("" = no downshift)
	}{
		{"catalog default may downshift", false, "medium"},
		{"explicit choice is respected", true, ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			eng := &Engine{client: &llm.Client{
				Model:        "m",
				Effort:       "high",
				EffortPinned: tc.pinned,
			}}

			restore := eng.applyAutoEffort("thanks")
			defer restore()

			eng.mu.Lock()
			got := eng.runEffort
			eng.mu.Unlock()

			if got != tc.want {
				t.Fatalf("pinned=%v runEffort=%q want %q", tc.pinned, got, tc.want)
			}
			if eng.client.Effort != "high" {
				t.Fatalf("selected tier mutated: %q", eng.client.Effort)
			}
		})
	}
}
