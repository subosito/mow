package proc

import (
	"strings"
	"testing"

	"github.com/subosito/mow/ext"
)

// The pack brings its own system-prompt segment: proc guidance must be
// present exactly because the pack is linked (never baked into the spine).
func TestProcRegistersSystemSegment(t *testing.T) {
	found := false
	for _, s := range ext.SystemSegments() {
		if strings.Contains(s, "proc_start") && strings.Contains(s, "NEVER") {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("proc system segment not registered")
	}
}
