package job

import (
	"os"
	"testing"
)

// Isolate tests from the developer's ~/.mow (config, skills, AGENTS) and from
// ambient provider credentials. Several CLI paths build an Engine, which fails
// without an API key — on a developer box the real key masks that, so CI (with
// no key) saw failures the local run did not. Pin both here.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "mow-home-test-*")
	if err != nil {
		panic(err)
	}
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
