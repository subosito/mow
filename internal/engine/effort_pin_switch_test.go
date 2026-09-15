package engine

import (
	"context"
	"testing"

	"github.com/subosito/mow/internal/llm"
)

// A pinned effort (/effort, session resume) must survive a lean model switch
// when the new model allows it, and land on the new default_effort otherwise.
// Clearing the pin on every lean pick snapped effort back to default mid-session.
func TestSetModelPreservesPinnedEffort(t *testing.T) {
	newEng := func(model, effort string, pinned bool) *Engine {
		c := &llm.Client{Model: model, Effort: effort, EffortPinned: pinned}
		c.SetCatalogModels([]llm.ModelInfo{
			{ID: "model-a", Efforts: []string{"low", "high"}, DefaultEffort: "low"},
			{ID: "model-b", Efforts: []string{"low", "high"}, DefaultEffort: "low"},
			{ID: "model-c", Efforts: []string{"low", "medium"}, DefaultEffort: "medium"},
		})
		eng := &Engine{client: c}
		eng.chat = func(context.Context, []llm.Message, []llm.ToolSpec) (llm.Message, error) {
			return llm.Message{}, nil
		}
		return eng
	}

	t.Run("pinned tier the new model allows is kept", func(t *testing.T) {
		eng := newEng("model-a", "high", true)
		if err := eng.SetModel("model-b"); err != nil {
			t.Fatal(err)
		}
		if got := eng.Effort(); got != "high" {
			t.Fatalf("Effort()=%q want high (pinned tier survives a lean switch)", got)
		}
	})

	t.Run("pinned tier the new model forbids falls to its default", func(t *testing.T) {
		eng := newEng("model-a", "high", true)
		if err := eng.SetModel("model-c"); err != nil {
			t.Fatal(err)
		}
		if got := eng.Effort(); got != "medium" {
			t.Fatalf("Effort()=%q want medium (new default_effort)", got)
		}
		if err := eng.SetModel("model-b"); err != nil {
			t.Fatal(err)
		}
		if got := eng.Effort(); got != "low" {
			t.Fatalf("Effort()=%q want low (catalog fallback must not stay pinned)", got)
		}
	})

	t.Run("unpinned catalog default adopts the new default", func(t *testing.T) {
		eng := newEng("model-a", "high", false)
		if err := eng.SetModel("model-c"); err != nil {
			t.Fatal(err)
		}
		if got := eng.Effort(); got != "medium" {
			t.Fatalf("Effort()=%q want medium (unpinned leftover follows the new model)", got)
		}
	})
}
