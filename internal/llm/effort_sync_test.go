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

func TestSyncEffortToModelNoCatalogEffortsClearsLeftover(t *testing.T) {
	c := &Client{Model: "model-a", Effort: "high"}
	c.SetCatalogModels([]ModelInfo{
		{ID: "model-b"}, // no efforts, no default_effort
	})
	c.SyncEffortToModel("model-b")
	if c.Effort != "" {
		t.Fatalf("effort = %q, want empty when catalog has no efforts", c.Effort)
	}
}

func TestSyncEffortToModelUnknownModelKeepsConfiguredEffort(t *testing.T) {
	c := &Client{Model: "model-a", Effort: "high"}
	c.SetCatalogModels([]ModelInfo{
		{ID: "model-a", Efforts: []string{"low", "high"}, DefaultEffort: "high"},
	})
	c.SyncEffortToModel("unknown-model")
	if c.Effort != "high" {
		t.Fatalf("effort = %q, want unchanged when model is not in the catalog", c.Effort)
	}
}

func TestSyncEffortToModelAdoptsLoneDefaultWithoutEfforts(t *testing.T) {
	c := &Client{Model: "model-a", Effort: "high"}
	c.SetCatalogModels([]ModelInfo{
		{ID: "model-b", DefaultEffort: "medium"},
	})
	c.SyncEffortToModel("model-b")
	if c.Effort != "medium" {
		t.Fatalf("effort = %q, want advertised default_effort", c.Effort)
	}
}
