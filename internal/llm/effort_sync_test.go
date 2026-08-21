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

func TestCatalogLookupToleratesProviderPrefix(t *testing.T) {
	efforts := []string{"low", "medium", "high"}
	cases := []struct {
		name      string
		catalogID string
		query     string
	}{
		{"query has cs/ catalog does not", "gemini-3.7-flash", "cs/gemini-3.7-flash"},
		{"catalog has cs/ query does not", "cs/gemini-3.7-flash", "gemini-3.7-flash"},
		{"both have cs/", "cs/gemini-3.7-flash", "cs/gemini-3.7-flash"},
		{"neither has prefix", "gemini-3.7-flash", "gemini-3.7-flash"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := &Client{}
			c.SetCatalogModels([]ModelInfo{{
				ID: tc.catalogID, Efforts: efforts, DefaultEffort: "high",
			}})
			info, ok := c.CatalogEntry(tc.query)
			if !ok {
				t.Fatalf("CatalogEntry(%q) miss (catalog %q)", tc.query, tc.catalogID)
			}
			if got := c.DefaultEffortForModel(tc.query); got != "high" {
				t.Fatalf("DefaultEffortForModel(%q)=%q want high", tc.query, got)
			}
			got := c.EffortsForModel(tc.query)
			if len(got) != 3 {
				t.Fatalf("EffortsForModel(%q)=%v", tc.query, got)
			}
			_ = info
		})
	}
}

func TestSyncEffortToModelAdoptsDefaultAcrossProviderPrefix(t *testing.T) {
	c := &Client{Model: "cs/gemini-3.7-flash"}
	c.SetCatalogModels([]ModelInfo{{
		ID: "gemini-3.7-flash", Efforts: []string{"low", "medium", "high"}, DefaultEffort: "high",
	}})
	c.SyncEffortToModel("cs/gemini-3.7-flash")
	if c.Effort != "high" {
		t.Fatalf("Effort=%q want catalog default high (cs/ prefix vs bare catalog id)", c.Effort)
	}
	if c.EffortPinned {
		t.Fatal("catalog default must stay unpinned")
	}
}
