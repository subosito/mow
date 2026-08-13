package engine

import (
	"context"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestSuggestEffortForPrompt(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		text    string
		current string
		allowed []string
		want    string
	}{
		{"leave low alone", "thanks", "low", nil, ""},
		{"leave medium alone", "thanks", "medium", nil, ""},
		{"leave empty alone", "thanks", "", nil, ""},
		{"downshift high short", "thanks", "high", nil, "medium"},
		{"downshift max short", "please rebuild", "max", []string{"low", "medium", "high", "max"}, "medium"},
		{"keep high when long", strings.Repeat("x", simplePromptMaxRunes+1), "high", nil, ""},
		{"keep high for design", "design the API boundary", "high", nil, ""},
		{"keep high for debug", "debug the race condition", "max", []string{"medium", "max"}, ""},
		{"keep for root cause", "root cause of the panic", "high", nil, ""},
		{"ok for please commit", "please commit", "high", nil, "medium"},
		{"ok for hi", "hi", "high", nil, "medium"},
		{"medium not in catalog", "thanks", "max", []string{"low", "high", "max"}, ""},
		{"boundary length", strings.Repeat("a", simplePromptMaxRunes), "high", nil, "medium"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := SuggestEffortForPrompt(tc.text, tc.current, tc.allowed)
			if got != tc.want {
				t.Fatalf("SuggestEffortForPrompt(%q, %q) = %q, want %q (runes=%d)",
					tc.text, tc.current, got, tc.want, utf8.RuneCountInString(tc.text))
			}
		})
	}
}

func TestIsHighCostEffort(t *testing.T) {
	t.Parallel()
	for _, e := range []string{"high", "HIGH", "max", "xhigh", "ultra"} {
		if !isHighCostEffort(e) {
			t.Errorf("isHighCostEffort(%q) = false", e)
		}
	}
	for _, e := range []string{"", "none", "low", "medium", "auto"} {
		if isHighCostEffort(e) {
			t.Errorf("isHighCostEffort(%q) = true", e)
		}
	}
}

func TestApplyAutoEffort_doesNotMutatePublicEffort(t *testing.T) {
	eng, err := New(Options{
		NoSession: true,
		Effort:    "high",
		Chat: func(ctx context.Context, messages []Message, tools []ToolSpec) (Message, error) {
			return Message{Role: "assistant", Content: "ok"}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()

	if eng.cfg == nil {
		t.Fatal("expected cfg")
	}
	eng.cfg.LLM.Effort = "high"

	restore := eng.applyAutoEffort("thanks")
	if got := eng.Effort(); got != "high" {
		t.Fatalf("during simple prompt Effort()=%q want high (session setting)", got)
	}
	if got := eng.requestEffort(); got != "medium" {
		t.Fatalf("during simple prompt requestEffort=%q want medium", got)
	}
	if eng.cfg.LLM.Effort != "high" {
		t.Fatalf("cfg effort mutated to %q", eng.cfg.LLM.Effort)
	}
	restore()
	if got := eng.Effort(); got != "high" {
		t.Fatalf("after restore Effort()=%q want high", got)
	}
	if got := eng.requestEffort(); got != "high" {
		t.Fatalf("after restore requestEffort=%q want high", got)
	}

	// Complex short prompt must not downshift.
	restore = eng.applyAutoEffort("debug the flaky test")
	if got := eng.Effort(); got != "high" {
		t.Fatalf("complex prompt Effort()=%q want high", got)
	}
	if got := eng.requestEffort(); got != "high" {
		t.Fatalf("complex prompt requestEffort=%q want high", got)
	}
	restore()
}
