package mcp

import (
	"os"
	"testing"
)

// Isolate tests from the developer's ~/.mow (config, skills, AGENTS) so the
// suite does not depend on local config — e.g. enabled media tools.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "mow-home-test-*")
	if err != nil {
		panic(err)
	}
	_ = os.Setenv("MOW_HOME", dir)
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}
