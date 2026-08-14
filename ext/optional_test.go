package ext

import "testing"

func TestOptionalFeatureRegistry(t *testing.T) {
	Reset()
	t.Cleanup(Reset)
	if got := OptionalFeatures(); len(got) != 0 {
		t.Fatalf("minimal registry = %+v, want no optional features", got)
	}

	RegisterOptionalFeature(OptionalFeature{
		ID:     "  Demo ",
		Events: []string{"event.one", "event.one", "  event.two "},
	})
	RegisterOptionalFeature(OptionalFeature{ID: "demo", Events: []string{"event.three"}})

	got := OptionalFeatures()
	if len(got) != 1 || got[0].ID != "demo" {
		t.Fatalf("OptionalFeatures() = %+v, want one replacement", got)
	}
	if len(got[0].Events) != 1 || got[0].Events[0] != "event.three" {
		t.Fatalf("events = %v, want replacement event", got[0].Events)
	}
}
