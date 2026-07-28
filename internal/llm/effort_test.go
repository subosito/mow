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

func TestStripEffortTier(t *testing.T) {
	if got := StripEffortTier("ag/gemini-3.6-flash-medium"); got != "ag/gemini-3.6-flash" {
		t.Fatalf("got %q", got)
	}
	if got := StripEffortTier("gpt-5-mini"); got != "gpt-5-mini" {
		t.Fatalf("got %q", got)
	}
}

func TestResolveEffortModelIDTier(t *testing.T) {
	catalog := []string{
		"ag/gemini-3.6-flash-low",
		"ag/gemini-3.6-flash-medium",
	}
	plan := ResolveEffort("ag/gemini-3.6-flash-medium", WireOpenAIChat, "high", catalog)
	// high missing → fallback medium
	if plan.Model != "ag/gemini-3.6-flash-medium" {
		t.Fatalf("model=%q", plan.Model)
	}
	if plan.ReasoningEffort != "" || plan.ThinkingBudget != nil {
		t.Fatalf("body fields should be empty for tier adapter: %+v", plan)
	}
	plan = ResolveEffort("ag/gemini-3.6-flash-medium", WireOpenAIChat, "low", catalog)
	if plan.Model != "ag/gemini-3.6-flash-low" {
		t.Fatalf("low: %q", plan.Model)
	}
	plan = ResolveEffort("ag/gemini-3.6-flash-medium", WireOpenAIChat, "none", catalog)
	// none prefers base; not in catalog → low
	if plan.Model != "ag/gemini-3.6-flash-low" {
		t.Fatalf("none: %q", plan.Model)
	}
}

func TestResolveEffortOptimisticAG(t *testing.T) {
	plan := ResolveEffort("ag/gemini-3.6-flash-medium", WireOpenAIChat, "high", nil)
	if plan.Model != "ag/gemini-3.6-flash-high" {
		t.Fatalf("got %q", plan.Model)
	}
}

func TestResolveEffortBodyReasoning(t *testing.T) {
	plan := ResolveEffort("gpt-5-mini", WireOpenAIChat, "high", nil)
	if plan.Model != "gpt-5-mini" || plan.ReasoningEffort != "high" {
		t.Fatalf("%+v", plan)
	}
}

func TestResolveEffortGeminiThinkingBudget(t *testing.T) {
	plan := ResolveEffort("gemini-3.6-flash", WireOpenAIChat, "low", nil)
	if plan.ThinkingBudget == nil || *plan.ThinkingBudget != 256 {
		t.Fatalf("%+v", plan)
	}
	if plan.ReasoningEffort != "" {
		t.Fatalf("prefer thinking_budget for gemini: %+v", plan)
	}
}

func TestResolveEffortUnsetKeepsExplicitTier(t *testing.T) {
	plan := ResolveEffort("ag/gemini-3.6-flash-medium", WireOpenAIChat, "", nil)
	if plan.Model != "ag/gemini-3.6-flash-medium" {
		t.Fatalf("%+v", plan)
	}
}

func TestResolveEffortBareAGDefaultsMedium(t *testing.T) {
	catalog := []string{"ag/gemini-3.6-flash-low", "ag/gemini-3.6-flash-medium"}
	plan := ResolveEffort("ag/gemini-3.6-flash", WireOpenAIChat, "", catalog)
	if plan.Model != "ag/gemini-3.6-flash-medium" {
		t.Fatalf("%+v", plan)
	}
}

func TestCollapseEffortTiersInCatalog(t *testing.T) {
	in := []ModelInfo{
		{ID: "ag/gemini-3.6-flash-low", Wire: "openai-chat-completions"},
		{ID: "ag/gemini-3.6-flash-medium", Wire: "openai-chat-completions"},
		{ID: "gpt-5-mini", Wire: "openai-chat-completions"},
		{ID: "deepseek-chat"},
	}
	got := CollapseEffortTiersInCatalog(in)
	want := map[string]bool{
		"ag/gemini-3.6-flash": true,
		"gpt-5-mini":          true,
		"deepseek-chat":       true,
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
	base, eff := NormalizeConfiguredModel("ag/gemini-3.6-flash-medium", "")
	if base != "ag/gemini-3.6-flash" || eff != "medium" {
		t.Fatalf("%q %q", base, eff)
	}
	base, eff = NormalizeConfiguredModel("ag/gemini-3.6-flash-medium", "high")
	if base != "ag/gemini-3.6-flash" || eff != "high" {
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
}
