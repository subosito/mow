package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/subosito/mow/internal/config"
)

func TestLoadDefaultsSecureTools(t *testing.T) {
	t.Setenv(config.EnvHome, t.TempDir()) // isolate from developer ~/.mow
	t.Setenv("OPENAI_API_KEY", "sk-test")
	t.Setenv("OPENAI_MODEL", "m")
	t.Setenv("OPENAI_BASE_URL", "http://example.com/v1")
	t.Setenv("MOW_API_KEY", "")
	t.Setenv("MOW_MODEL", "")
	t.Setenv("MOW_BASE_URL", "")
	// clear power env

	f, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if !f.ToolEnabled("read") || !f.ToolEnabled("glob") {
		t.Fatalf("enable=%v", f.Tools.Enable)
	}
	if f.ToolEnabled("bash") || f.ToolEnabled("write") {
		t.Fatalf("power tools should be off: %v", f.Tools.Enable)
	}
	if f.ResolveAPIKey() != "sk-test" {
		t.Fatal("api key")
	}
	if f.LLM.Model != "m" {
		t.Fatal("model")
	}
}

func TestLoadYAMLAndEnv(t *testing.T) {
	t.Setenv(config.EnvHome, t.TempDir())
	dir := t.TempDir()
	path := filepath.Join(dir, "c.yaml")
	if err := os.WriteFile(path, []byte("llm:\n  model: from-yaml\ntools:\n  enable:\n    - read\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OPENAI_API_KEY", "k")
	t.Setenv("OPENAI_MODEL", "from-env") // env wins after file in applyEnv
	t.Setenv("MOW_API_KEY", "")
	t.Setenv("MOW_MODEL", "")
	t.Setenv("MOW_BASE_URL", "")
	f, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if f.LLM.Model != "from-env" {
		t.Fatalf("model=%q want from-env", f.LLM.Model)
	}
	if f.ToolEnabled("glob") {
		t.Fatal("yaml enable should replace defaults")
	}
}

func TestMaxTurnsUnlimitedYAML(t *testing.T) {
	t.Setenv(config.EnvHome, t.TempDir())
	dir := t.TempDir()
	path := filepath.Join(dir, "c.yaml")
	if err := os.WriteFile(path, []byte("policy:\n  max_turns: -1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OPENAI_API_KEY", "k")
	t.Setenv("OPENAI_MODEL", "m")
	f, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if f.Policy.MaxTurns != 0 {
		t.Fatalf("MaxTurns=%d want 0 (unlimited)", f.Policy.MaxTurns)
	}
}

func TestExtensionsSection(t *testing.T) {
	t.Setenv(config.EnvHome, t.TempDir())
	dir := t.TempDir()
	path := filepath.Join(dir, "c.yaml")
	yaml := `
llm:
  model: m
extensions:
  demo:
    welcome: false
    welcome_message: custom hi
  other:
    x: 1
`
	if err := os.WriteFile(path, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OPENAI_API_KEY", "k")
	t.Setenv("OPENAI_MODEL", "m")
	f, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	var demo struct {
		Welcome        *bool  `yaml:"welcome"`
		WelcomeMessage string `yaml:"welcome_message"`
	}
	if err := f.Extension("demo", &demo); err != nil {
		t.Fatal(err)
	}
	if demo.Welcome == nil || *demo.Welcome {
		t.Fatalf("welcome=%v", demo.Welcome)
	}
	if demo.WelcomeMessage != "custom hi" {
		t.Fatalf("msg=%q", demo.WelcomeMessage)
	}
	// missing section is no-op
	var empty struct {
		Z int `yaml:"z"`
	}
	if err := f.Extension("nope", &empty); err != nil {
		t.Fatal(err)
	}
	if empty.Z != 0 {
		t.Fatal(empty.Z)
	}
}

func TestLoadGenerateUnderstandYAML(t *testing.T) {
	t.Setenv(config.EnvHome, t.TempDir())
	dir := t.TempDir()
	path := filepath.Join(dir, "c.yaml")
	yaml := `
llm:
  model: deepseek-v4-flash
  generate:
    image: grok-imagine-image-quality
  understand:
    image: qwen-vl
tools:
  enable:
    - read
    - glob
    - grep
    - generate_image
    - understand_image
`
	if err := os.WriteFile(path, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OPENAI_API_KEY", "k")
	t.Setenv("OPENAI_MODEL", "") // leave yaml model
	// clear model env if set
	t.Setenv("MOW_MODEL", "")
	f, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	// env OPENAI_MODEL empty string might not clear - Load applies firstEnv only if non-empty
	if f.LLM.Generate.Image != "grok-imagine-image-quality" {
		t.Fatalf("generate.image=%q", f.LLM.Generate.Image)
	}
	if f.LLM.Understand.Image != "qwen-vl" {
		t.Fatalf("understand.image=%q", f.LLM.Understand.Image)
	}
	if !f.ToolEnabled("generate_image") || !f.ToolEnabled("understand_image") {
		t.Fatalf("enable=%v", f.Tools.Enable)
	}
}
