package engine

import (
	"os"
	"testing"
)

// Isolate engine tests from the developer's ~/.mow (config, skills, AGENTS).
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "mow-engine-test-*")
	if err != nil {
		panic(err)
	}
	_ = os.Setenv("MOW_HOME", dir)
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}
