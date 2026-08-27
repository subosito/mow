package cli

import (
	"strings"
	"testing"

	"github.com/subosito/mow"
)

func TestFormatModelCatalogMarksCurrentAndDefaultEffort(t *testing.T) {
	out := formatModelCatalog([]mow.ModelInfo{
		{
			ID:            "gpt-5-mini",
			Wire:          "openai-responses",
			Wires:         []string{"openai-responses", "openai-chat-completions"},
			Efforts:       []string{"none", "low", "medium", "high"},
			DefaultEffort: "medium",
		},
		{
			ID:   "deepseek-chat",
			Wire: "openai-chat-completions",
		},
	}, "gpt-5-mini", "openai-responses")
	for _, want := range []string{
		"models  2",
		"current gpt-5-mini",
		"wire openai-responses",
		"MODEL",
		"WIRE",
		"EFFORTS",
		"• gpt-5-mini",
		"openai-responses (+openai-chat-completions)",
		"none, low, medium*, high",
		"  deepseek-chat",
		"—",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

func TestFilterModelsMatchesIDAndWire(t *testing.T) {
	list := []mow.ModelInfo{
		{ID: "gpt-5-mini", Wire: "openai-responses"},
		{ID: "claude-sonnet-4", Wire: "anthropic-messages"},
		{ID: "deepseek-chat", Wire: "openai-chat-completions"},
	}
	got := filterModels(list, "sonnet")
	if len(got) != 1 || got[0].ID != "claude-sonnet-4" {
		t.Fatalf("id filter: %+v", got)
	}
	got = filterModels(list, "anthropic")
	if len(got) != 1 || got[0].ID != "claude-sonnet-4" {
		t.Fatalf("wire filter: %+v", got)
	}
}

func TestFormatModelEffortsDefaultOnly(t *testing.T) {
	if got := formatModelEfforts(mow.ModelInfo{DefaultEffort: "low"}); got != "low*" {
		t.Fatalf("got %q", got)
	}
	if got := formatModelEfforts(mow.ModelInfo{}); got != "—" {
		t.Fatalf("empty got %q", got)
	}
}
