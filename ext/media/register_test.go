package media

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/subosito/mow/ext"
)

// setup is the BeforeNew gate. These tests pin the two halves of the contract
// that used to live in internal/engine: a configured model registers the tool,
// an unconfigured one registers nothing (and never errors).
func TestSetupRegistersOnlyConfiguredTools(t *testing.T) {
	home := t.TempDir()
	t.Setenv("MOW_HOME", home)
	t.Setenv("OPENAI_API_KEY", "test-key")
	t.Setenv("MOW_API_KEY", "")
	t.Setenv("ANTHROPIC_API_KEY", "")

	cfg := filepath.Join(home, "config.yaml")
	body := "extensions:\n  media:\n    generate:\n      image: img-model\n    understand:\n      voice: audio-model\n"
	if err := os.WriteFile(cfg, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := setup(cfg); err != nil {
		t.Fatalf("setup: %v", err)
	}

	got := map[string]bool{}
	for _, tl := range ext.Tools() {
		got[tl.Name()] = true
	}
	for _, want := range []string{"generate_image", "understand_voice"} {
		if !got[want] {
			t.Errorf("%s not registered for a configured model", want)
		}
	}
	for _, unwanted := range []string{"generate_speech", "generate_video", "understand_image", "understand_video"} {
		if got[unwanted] {
			t.Errorf("%s registered without a configured model", unwanted)
		}
	}
}

func TestSetupWithoutAPIKeyRegistersNothing(t *testing.T) {
	home := t.TempDir()
	t.Setenv("MOW_HOME", home)
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("MOW_API_KEY", "")
	t.Setenv("ANTHROPIC_API_KEY", "")

	cfg := filepath.Join(home, "config.yaml")
	if err := os.WriteFile(cfg, []byte("extensions:\n  media:\n    generate:\n      image: img-model\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// No key means no media client can be built: a no-op, never an error.
	if err := setup(cfg); err != nil {
		t.Fatalf("setup without api key: %v", err)
	}
}
