package review

import (
	"os"
	"testing"
)

// Isolate package tests from the developer's ~/.mow (config, skills, AGENTS).
// The command tests build real engines, so without this they would pick up
// local model/base-url config and behave differently on every machine.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "mow-review-home-*")
	if err != nil {
		panic(err)
	}
	_ = os.Setenv("MOW_HOME", dir)
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}
