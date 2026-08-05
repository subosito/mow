package mowi

import (
	"os"
	"testing"
)

// Isolate tests from the developer's ~/.mow (config, skills, AGENTS) so the
// suite does not depend on local config — e.g. enabled media tools would make
// the test engine's mow.New fail. Per-test t.Setenv still overrides this.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "mowi-home-test-*")
	if err != nil {
		panic(err)
	}
	_ = os.Setenv("MOW_HOME", dir)
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}
