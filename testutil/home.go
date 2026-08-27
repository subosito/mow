package testutil

import (
	"os"
	"testing"
)

// Run pins HOME and MOW_HOME to a temp dir, runs m, then exits.
func Run(m *testing.M) {
	dir, err := os.MkdirTemp("", "mow-home-*")
	if err != nil {
		panic(err)
	}
	_ = os.Setenv("HOME", dir)
	_ = os.Setenv("MOW_HOME", dir)
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

// RunWithProvider is Run plus fake credentials for Engine construction on CI.
func RunWithProvider(m *testing.M) {
	dir, err := os.MkdirTemp("", "mow-home-*")
	if err != nil {
		panic(err)
	}
	_ = os.Setenv("HOME", dir)
	_ = os.Setenv("MOW_HOME", dir)
	_ = os.Setenv("MOW_API_KEY", "test-key")
	_ = os.Setenv("MOW_MODEL", "gpt-5-mini")
	_ = os.Unsetenv("OPENAI_API_KEY")
	_ = os.Unsetenv("ANTHROPIC_API_KEY")
	_ = os.Unsetenv("OPENAI_MODEL")
	_ = os.Unsetenv("ANTHROPIC_MODEL")
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}
