package ext_test

import (
	"strings"
	"testing"

	"github.com/subosito/mow/ext"
)

func TestSystemSegmentsRoundTrip(t *testing.T) {
	ext.Reset()
	t.Cleanup(ext.Reset)

	ext.RegisterSystemSegment(func(paths ...string) string { return "proc advice" })
	ext.RegisterSystemSegment(func(paths ...string) string { return "   " }) // empty → dropped
	ext.RegisterSystemSegmentSource("mcp", func(paths ...string) string { return "mcp advice" })

	got := ext.SystemSegments()
	if len(got) != 2 {
		t.Fatalf("SystemSegments()=%v, want 2 entries", got)
	}
	if got[0] != "proc advice" || got[1] != "mcp advice" {
		t.Fatalf("segments=%v", got)
	}

	// Config paths reach the provider (registration order preserved).
	var seen []string
	ext.Reset()
	ext.RegisterSystemSegment(func(paths ...string) string {
		seen = append([]string(nil), paths...)
		return "ok"
	})
	ext.SystemSegments("/a.yaml", "/b.yaml")
	if strings.Join(seen, ",") != "/a.yaml,/b.yaml" {
		t.Fatalf("provider saw %v", seen)
	}
}

func TestSystemSegmentsClearBySource(t *testing.T) {
	ext.Reset()
	t.Cleanup(ext.Reset)

	ext.RegisterSystemSegmentSource("proc", func(paths ...string) string { return "proc" })
	ext.RegisterSystemSegmentSource("other", func(paths ...string) string { return "other" })

	ext.ClearHookSource("proc")
	got := ext.SystemSegments()
	if len(got) != 1 || got[0] != "other" {
		t.Fatalf("after clear: %v, want only other", got)
	}

	ext.Reset()
	if got := ext.SystemSegments(); len(got) != 0 {
		t.Fatalf("after reset: %v, want none", got)
	}
}
