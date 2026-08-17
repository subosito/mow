package engine

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestFallbackVersionMatchesVERSIONFile(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("no caller")
	}
	root := filepath.Join(filepath.Dir(file), "..", "..")
	raw, err := os.ReadFile(filepath.Join(root, "VERSION"))
	if err != nil {
		t.Fatal(err)
	}
	want := strings.TrimSpace(string(raw))
	if Version != want {
		t.Fatalf("engine.Version=%q VERSION file=%q — keep them in lockstep (ldflags overrides the var on release builds)", Version, want)
	}
}
