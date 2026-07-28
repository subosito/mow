package llm

import (
	"encoding/json"
	"testing"
)

func TestNormalizeEffort(t *testing.T) {
	cases := []struct {
		in, want string
		err      bool
	}{
		{"", "", false},
		{"auto", "", false},
		{"LOW", "low", false},
		{"medium", "medium", false},
		{"high", "high", false},
		{"none", "none", false},
		{"off", "none", false},
		{"banana", "", true},
	}
	for _, tc := range cases {
		got, err := NormalizeEffort(tc.in)
		if tc.err {
			if err == nil {
				t.Fatalf("%q: want error", tc.in)
			}
			continue
		}
		if err != nil || got != tc.want {
			t.Fatalf("%q: got %q err=%v want %q", tc.in, got, err, tc.want)
		}
	}
}

func TestStripEffortTiers(t *testing.T) {
	if got := StripEffortTiers("gemini-3.6-flash-medium"); got != "gemini-3.6-flash" {
		t.Fatalf("got %q", got)
	}
	if got := StripEffortTiers("gpt-5-mini"); got != "gpt-5-mini" {
		t.Fatalf("got %q", got)
	}
}

func TestResolveEffortNeverRewritesModelTier(t *testing.T) {
	// Effort is body-only; model stays lean even if catalog has legacy tiers.
	catalog := []string{"gemini-3.6-flash-low", "gemini-3.6-flash-medium"}
	plan := ResolveEffort("gemini-3.6-flash", WireOpenAIChat, "high", catalog)
	if plan.Model != "gemini-3.6-flash" {
		t.Fatalf("model rewritten: %q", plan.Model)
	}
	if plan.ThinkingBudget == nil || *plan.ThinkingBudget != 8192 {
		t.Fatalf("thinking_budget=%v", plan.ThinkingBudget)
	}
	if plan.ReasoningEffort != "" {
		t.Fatalf("unexpected reasoning_effort for gemini: %q", plan.ReasoningEffort)
	}
}

func TestResolveEffortStripsLegacyTierOnWire(t *testing.T) {
	plan := ResolveEffort("gemini-3.6-flash-medium", WireOpenAIChat, "low", nil)
	if plan.Model != "gemini-3.6-flash" {
		t.Fatalf("want lean model, got %q", plan.Model)
	}
	if plan.ThinkingBudget == nil || *plan.ThinkingBudget != 256 {
		t.Fatalf("%+v", plan)
	}
}

func TestResolveEffortBodyReasoning(t *testing.T) {
	plan := ResolveEffort("gpt-5-mini", WireOpenAIChat, "high", nil)
	if plan.Model != "gpt-5-mini" || plan.ReasoningEffort != "high" {
		t.Fatalf("%+v", plan)
	}
}

func TestResolveEffortUnset(t *testing.T) {
	plan := ResolveEffort("gemini-3.6-flash", WireOpenAIChat, "", nil)
	if plan.Model != "gemini-3.6-flash" || plan.ThinkingBudget != nil || plan.ReasoningEffort != "" {
		t.Fatalf("%+v", plan)
	}
}

func TestCollapseEffortTiersInCatalog(t *testing.T) {
	in := []ModelInfo{
		{ID: "gemini-3.6-flash-low", Wire: "openai-chat-completions"},
		{ID: "gemini-3.6-flash-medium", Wire: "openai-chat-completions"},
		{ID: "gpt-5-mini", Wire: "openai-chat-completions"},
		{ID: "deepseek-chat"},
	}
	got := CollapseEffortTiersInCatalog(in)
	want := map[string]bool{
		"gemini-3.6-flash": true,
		"gpt-5-mini":       true,
		"deepseek-chat":    true,
	}
	if len(got) != len(want) {
		t.Fatalf("got %d: %+v", len(got), got)
	}
	for _, m := range got {
		if !want[m.ID] {
			t.Fatalf("unexpected %q", m.ID)
		}
	}
}

func TestNormalizeConfiguredModel(t *testing.T) {
	base, eff := NormalizeConfiguredModel("gemini-3.6-flash-medium", "")
	if base != "gemini-3.6-flash" || eff != "medium" {
		t.Fatalf("%q %q", base, eff)
	}
	base, eff = NormalizeConfiguredModel("gemini-3.6-flash-medium", "high")
	if base != "gemini-3.6-flash" || eff != "high" {
		t.Fatalf("effort wins: %q %q", base, eff)
	}
}

func TestFinalizeChatBody(t *testing.T) {
	c := &Client{Model: "gpt-5-mini", Wire: WireOpenAIChat, Effort: "medium"}
	raw, _ := json.Marshal(map[string]any{"model": "gpt-5-mini", "stream": false})
	out, err := c.finalizeChatBody(raw)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatal(err)
	}
	if m["reasoning_effort"] != "medium" {
		t.Fatalf("%v", m)
	}
	if m["model"] != "gpt-5-mini" {
		t.Fatalf("model=%v", m["model"])
	}
}

func TestFinalizeChatBodyGeminiThinkingBudget(t *testing.T) {
	c := &Client{Model: "gemini-3.6-flash-medium", Wire: WireOpenAIChat, Effort: "high"}
	raw, _ := json.Marshal(map[string]any{"model": c.requestModel()})
	out, err := c.finalizeChatBody(raw)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	_ = json.Unmarshal(out, &m)
	if m["model"] != "gemini-3.6-flash" {
		t.Fatalf("model=%v", m["model"])
	}
	if m["thinking_budget"] != float64(8192) {
		t.Fatalf("%v", m)
	}
}
