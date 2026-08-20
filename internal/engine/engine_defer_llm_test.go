package engine

import (
	"strings"
	"testing"
)

func TestNewDeferLLMSkipsAPIKey(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("MOW_API_KEY", "")
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("MOW_HOME", t.TempDir())

	eng, err := New(Options{DeferLLM: true, NoSession: true, LoadUserConfig: false})
	if err != nil {
		t.Fatalf("New(DeferLLM): %v", err)
	}
	defer eng.Close()

	_, err = eng.Prompt(t.Context(), "hi")
	if err == nil {
		t.Fatal("Prompt without a key must fail")
	}
	if !strings.Contains(err.Error(), "api key required") {
		t.Fatalf("Prompt error = %v; want api key required", err)
	}
}
