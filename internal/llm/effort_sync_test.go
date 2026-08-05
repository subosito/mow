package llm

import "testing"

func TestSyncEffortToModelAdoptsDefaultOnModelSwitch(t *testing.T) {
	c := &Client{Model: "model-a", Effort: "max"}
	c.SetCatalogModels([]ModelInfo{
		{ID: "model-a", Efforts: []string{"low", "high", "max"}, DefaultEffort: "high"},
		{ID: "model-b", Efforts: []string{"low", "medium", "high", "max"}, DefaultEffort: "medium"},
	})
	// A catalog-derived effort must not leak into the next model.
	c.SyncEffortToModel("model-b")
	if c.Effort != "medium" {
		t.Fatalf("model switch effort = %q, want model-b default_effort medium", c.Effort)
	}
}

func TestSyncEffortToModelKeepsPinnedEffort(t *testing.T) {
	c := &Client{Model: "model-a", Effort: "max", EffortPinned: true}
	c.SetCatalogModels([]ModelInfo{
		{ID: "model-b", Efforts: []string{"low", "medium", "max"}, DefaultEffort: "medium"},
	})
	c.SyncEffortToModel("model-b")
	if c.Effort != "max" {
		t.Fatalf("pinned effort = %q, want max preserved", c.Effort)
	}
}

func TestSyncEffortToModelDropsPinnedEffortWhenUnsupported(t *testing.T) {
	c := &Client{Model: "model-a", Effort: "max", EffortPinned: true}
	c.SetCatalogModels([]ModelInfo{
		{ID: "model-b", Efforts: []string{"low", "medium"}, DefaultEffort: "medium"},
	})
	c.SyncEffortToModel("model-b")
	if c.Effort != "medium" {
		t.Fatalf("unsupported pinned effort = %q, want default medium", c.Effort)
	}
}

func TestSyncEffortToModelNoCatalogEffortsIsNoop(t *testing.T) {
	c := &Client{Model: "model-a", Effort: "high"}
	c.SyncEffortToModel("model-b")
	if c.Effort != "high" {
		t.Fatalf("effort = %q, want unchanged without catalog metadata", c.Effort)
	}
}
