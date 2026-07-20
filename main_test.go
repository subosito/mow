package mow_test

import (
	"os"
	"testing"
)

// Isolate package tests from the developer's ~/.mow (config, skills, AGENTS).
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
