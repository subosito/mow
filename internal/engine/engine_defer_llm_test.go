package engine

import (
	"os"
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

func TestNewSkipsUnconfiguredMediaTools(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("MOW_API_KEY", "")
	t.Setenv("ANTHROPIC_API_KEY", "")
	home := t.TempDir()
	t.Setenv("MOW_HOME", home)
	cfg := home + "/config.yaml"
	if err := os.WriteFile(cfg, []byte("tools:\n  enable: [read, glob, grep, generate_image]\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	eng, err := New(Options{
		DeferLLM:       true,
		NoSession:      true,
		LoadUserConfig: true,
		ConfigPaths:    []string{cfg},
	})
	if err != nil {
		t.Fatalf("New with generate_image enabled but no media model: %v", err)
	}
	defer eng.Close()
}
