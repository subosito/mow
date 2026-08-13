package rpc

import (
	"os"
	"testing"
)

// readSource returns a file from this package for tests that assert on the
// shape of the code itself (call sites, dispatch labels).
func readSource(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(b)
}
