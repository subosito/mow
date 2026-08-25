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

func TestNormalizeEffortConfiguredAcceptsCatalogOnlyTiers(t *testing.T) {
	// The static none|low|medium|high set is a no-catalog fallback. A model
	// that advertises max / xhigh must not be rejected before GET /models has
	// even run — that killed `mow acp` during initialize.
	cases := []struct {
		in, want string
		err      bool
	}{
		{"", "", false},
		{"auto", "", false},
		{"default", "", false},
		{"off", "none", false},
		{"min", "none", false},
		{"HIGH", "high", false},
		{"max", "max", false},
		{"xhigh", "xhigh", false},
		{"ultra", "ultra", false},
		{"x-high", "x-high", false},
		{"very high", "", true},
		{"high;rm -rf", "", true},
	}
	for _, tc := range cases {
		got, err := NormalizeEffortConfigured(tc.in)
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
	if got := StripEffortTiers("gemini-2.5-flash-medium"); got != "gemini-2.5-flash" {
		t.Fatalf("got %q", got)
	}
	if got := StripEffortTiers("gpt-5-mini"); got != "gpt-5-mini" {
		t.Fatalf("got %q", got)
	}
}

func TestResolveEffortNeverRewritesModelTier(t *testing.T) {
	// Effort is body-only; model stays lean even if catalog has legacy tiers.
	plan := ResolveEffort("gemini-2.5-flash", WireOpenAIChat, "high", nil)
	if plan.Model != "gemini-2.5-flash" {
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
	plan := ResolveEffort("gemini-2.5-flash-medium", WireOpenAIChat, "low", nil)
	if plan.Model != "gemini-2.5-flash" {
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
	plan := ResolveEffort("gemini-2.5-flash", WireOpenAIChat, "", nil)
	if plan.Model != "gemini-2.5-flash" || plan.ThinkingBudget != nil || plan.ReasoningEffort != "" {
		t.Fatalf("%+v", plan)
	}
}

func TestCollapseEffortTiersInCatalog(t *testing.T) {
	in := []ModelInfo{
		{ID: "gemini-2.5-flash-low", Wire: "openai-chat-completions"},
		{ID: "gemini-2.5-flash-medium", Wire: "openai-chat-completions"},
		{ID: "gpt-5-mini", Wire: "openai-chat-completions"},
		{ID: "deepseek-chat"},
	}
	got := CollapseEffortTiersInCatalog(in)
	want := map[string]bool{
		"gemini-2.5-flash": true,
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
	base, eff := NormalizeConfiguredModel("gemini-2.5-flash-medium", "")
	if base != "gemini-2.5-flash" || eff != "medium" {
		t.Fatalf("%q %q", base, eff)
	}
	base, eff = NormalizeConfiguredModel("gemini-2.5-flash-medium", "high")
	if base != "gemini-2.5-flash" || eff != "high" {
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
	// Legacy path: no catalog efforts → thinking_budget for Gemini family.
	c := &Client{Model: "gemini-2.5-flash-medium", Wire: WireOpenAIChat, Effort: "high"}
	raw, _ := json.Marshal(map[string]any{"model": c.requestModel()})
	out, err := c.finalizeChatBody(raw)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	_ = json.Unmarshal(out, &m)
	if m["model"] != "gemini-2.5-flash" {
		t.Fatalf("model=%v", m["model"])
	}
	if m["thinking_budget"] != float64(8192) {
		t.Fatalf("%v", m)
	}
}

func TestFinalizeChatBodyGeminiWithCatalogEfforts(t *testing.T) {
	// When gateway advertises efforts, send reasoning_effort (gateway maps tiers).
	c := &Client{Model: "gemini-2.5-flash", Wire: WireOpenAIChat, Effort: "high"}
	c.SetCatalogModels([]ModelInfo{{
		ID: "gemini-2.5-flash", Efforts: []string{"low", "medium", "high"}, DefaultEffort: "medium",
	}})
	raw, _ := json.Marshal(map[string]any{"model": "gemini-2.5-flash"})
	out, err := c.finalizeChatBody(raw)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	_ = json.Unmarshal(out, &m)
	if m["reasoning_effort"] != "high" {
		t.Fatalf("%v", m)
	}
	if m["thinking_budget"] != nil {
		t.Fatalf("should not set thinking_budget when catalog efforts present: %v", m)
	}
}

func TestFinalizeChatBodyCSGeminiCatalogHigh(t *testing.T) {
	// Prefixed request id + bare catalog id must still send reasoning_effort,
	// not the Gemini-family thinking_budget fallback the Cursor adapter ignores.
	c := &Client{Model: "cs/gemini-3.7-flash", Wire: WireOpenAIChat, Effort: "high"}
	c.SetCatalogModels([]ModelInfo{{
		ID: "gemini-3.7-flash", Efforts: []string{"low", "medium", "high"}, DefaultEffort: "high",
	}})
	raw, _ := json.Marshal(map[string]any{"model": "cs/gemini-3.7-flash"})
	out, err := c.finalizeChatBody(raw)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	_ = json.Unmarshal(out, &m)
	if m["reasoning_effort"] != "high" {
		t.Fatalf("reasoning_effort=%v want high; body=%v", m["reasoning_effort"], m)
	}
	if m["thinking_budget"] != nil {
		t.Fatalf("must not send thinking_budget when catalog efforts resolve: %v", m)
	}
	if m["model"] != "cs/gemini-3.7-flash" {
		t.Fatalf("model=%v", m["model"])
	}
}

func TestNormalizeEffortForCatalog(t *testing.T) {
	allowed := []string{"low", "medium", "high"}
	got, err := NormalizeEffortFor("high", allowed)
	if err != nil || got != "high" {
		t.Fatalf("%q %v", got, err)
	}
	if _, err := NormalizeEffortFor("none", allowed); err == nil {
		t.Fatal("none not in catalog efforts should error")
	}
	if _, err := NormalizeEffortFor("none", nil); err != nil {
		t.Fatal(err)
	}
}
