package engine

import (
	"strings"
	"testing"

	"github.com/subosito/mow/ext"
)

// Pack system segments (ext.RegisterSystemSegment) must reach the engine's
// compiled system prompt: guidance travels with the capability.
func TestExtSystemSegmentsReachSystemPrompt(t *testing.T) {
	ext.Reset()
	t.Cleanup(ext.Reset)

	const marker = "seam-test: use the registered tool"
	ext.RegisterSystemSegment(func(paths ...string) string { return marker })
	ext.RegisterSystemSegment(func(paths ...string) string { return "   " }) // dropped

	eng, err := New(Options{
		Workspace: t.TempDir(),
		NoSession: true,
		Chat:      hermeticChat(),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()

	if !strings.Contains(eng.sys, marker) {
		t.Fatalf("system prompt missing ext segment:\n%.300s", eng.sys)
	}
	if n := strings.Count(eng.sys, marker); n != 1 {
		t.Fatalf("segment appeared %d times, want once", n)
	}
}
