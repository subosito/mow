package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/subosito/mow/internal/config"
)

// writeProfileConfig creates $MOW_HOME/workspaces/<name>/{workspace.yaml,config.yaml}.
func writeProfileConfig(t *testing.T, home, name, workspace, configBody string) {
	t.Helper()
	dir := filepath.Join(home, "workspaces", name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "workspace.yaml"), []byte("root: "+workspace+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if configBody != "" {
		if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(configBody), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestLoadWithProfilePrecedence(t *testing.T) {
	home := t.TempDir()
	workspace := t.TempDir()
	t.Setenv(config.EnvHome, home)
	t.Setenv("OPENAI_API_KEY", "k")
	t.Setenv("OPENAI_MODEL", "")
	t.Setenv("MOW_MODEL", "")
	t.Setenv("MOW_API_KEY", "")

	if err := os.WriteFile(filepath.Join(home, "config.yaml"), []byte("llm:\n  model: global-model\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeProfileConfig(t, home, "monorepo", workspace, "llm:\n  model: profile-model\n")

	// Profile beats global.
	f, err := config.LoadWithProfile("monorepo")
	if err != nil {
		t.Fatal(err)
	}
	if f.LLM.Model != "profile-model" {
		t.Fatalf("model=%q want profile-model (profile over global)", f.LLM.Model)
	}

	// Explicit --config beats profile.
	explicit := filepath.Join(t.TempDir(), "explicit.yaml")
	if err := os.WriteFile(explicit, []byte("llm:\n  model: explicit-model\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f2, err := config.LoadWithProfile("monorepo", explicit)
	if err != nil {
		t.Fatal(err)
	}
	if f2.LLM.Model != "explicit-model" {
		t.Fatalf("model=%q want explicit-model (explicit --config over profile)", f2.LLM.Model)
	}

	// Env beats files.
	t.Setenv("MOW_MODEL", "env-model")
	f3, err := config.LoadWithProfile("monorepo", explicit)
	if err != nil {
		t.Fatal(err)
	}
	if f3.LLM.Model != "env-model" {
		t.Fatalf("model=%q want env-model (env over files)", f3.LLM.Model)
	}
}

func TestLoadWithProfileExtensionsAcpWholeSectionReplace(t *testing.T) {
	// Regression: profile extensions.acp must replace the global section, not
	// leave global peer models in place when the same agent name is redefined.
	home := t.TempDir()
	workspace := t.TempDir()
	t.Setenv(config.EnvHome, home)
	t.Setenv("OPENAI_API_KEY", "k")
	t.Setenv("OPENAI_MODEL", "m")
	t.Setenv("MOW_MODEL", "")
	t.Setenv("MOW_API_KEY", "")

	global := `
extensions:
  acp:
    mow_agents:
      deepseek:
        model: deepseek-v4-flash
      only-global:
        model: global-only-model
`
	if err := os.WriteFile(filepath.Join(home, "config.yaml"), []byte(global), 0o600); err != nil {
		t.Fatal(err)
	}
	profileBody := `
extensions:
  acp:
    mow_agents:
      deepseek:
        model: gateway/deepseek/deepseek-chat
`
	writeProfileConfig(t, home, "gateway-profile", workspace, profileBody)

	f, err := config.LoadWithProfile("gateway-profile")
	if err != nil {
		t.Fatal(err)
	}
	type acpSection struct {
		MowAgents map[string]struct {
			Model string `yaml:"model"`
		} `yaml:"mow_agents"`
	}
	var sec acpSection
	if err := f.Extension("acp", &sec); err != nil {
		t.Fatal(err)
	}
	got := sec.MowAgents["deepseek"].Model
	want := "gateway/deepseek/deepseek-chat"
	if got != want {
		t.Fatalf("deepseek model=%q want %q", got, want)
	}
	if _, ok := sec.MowAgents["only-global"]; ok {
		t.Fatalf("profile extensions.acp must whole-section replace global; still has only-global: %+v", sec.MowAgents)
	}
}

func TestLoadExplicitPathBeatsGlobal(t *testing.T) {
	// Explicit --config paths must win over $MOW_HOME/config.yaml (not the reverse).
	home := t.TempDir()
	t.Setenv(config.EnvHome, home)
	t.Setenv("OPENAI_API_KEY", "k")
	t.Setenv("OPENAI_MODEL", "")
	t.Setenv("MOW_MODEL", "")
	t.Setenv("MOW_API_KEY", "")

	if err := os.WriteFile(filepath.Join(home, "config.yaml"), []byte("llm:\n  model: global-model\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	explicit := filepath.Join(t.TempDir(), "c.yaml")
	if err := os.WriteFile(explicit, []byte("llm:\n  model: path-model\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := config.Load(explicit)
	if err != nil {
		t.Fatal(err)
	}
	if f.LLM.Model != "path-model" {
		t.Fatalf("model=%q want path-model (explicit path over global)", f.LLM.Model)
	}
}

func TestProfilePluginsDir(t *testing.T) {
	home := t.TempDir()
	workspace := t.TempDir()
	t.Setenv(config.EnvHome, home)
	writeProfileConfig(t, home, "monorepo", workspace, "")
	p, found, err := config.LoadProfile("monorepo")
	if err != nil || !found {
		t.Fatalf("LoadProfile: found=%v err=%v", found, err)
	}
	if p.HasPlugins() {
		t.Fatal("expected HasPlugins=false before plugins dir created")
	}
	if err := os.MkdirAll(p.PluginsDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if !p.HasPlugins() {
		t.Fatal("expected HasPlugins=true after plugins dir created")
	}
}

func TestOverlayConfigPathsOrdersProfileBeforeExplicit(t *testing.T) {
	home := t.TempDir()
	workspace := t.TempDir()
	t.Setenv(config.EnvHome, home)
	writeProfileConfig(t, home, "monorepo", workspace, "llm:\n  model: profile-model\n")
	p, found, err := config.LoadProfile("monorepo")
	if err != nil || !found {
		t.Fatalf("LoadProfile: found=%v err=%v", found, err)
	}
	explicit := filepath.Join(t.TempDir(), "e.yaml")
	paths := p.OverlayConfigPaths([]string{explicit})
	if len(paths) != 2 {
		t.Fatalf("paths=%v", paths)
	}
	if !strings.HasSuffix(paths[0], filepath.Join("workspaces", "monorepo", "config.yaml")) {
		t.Fatalf("first path should be profile config, got %q", paths[0])
	}
	if paths[1] != explicit {
		t.Fatalf("second path should be explicit, got %q", paths[1])
	}
}

func TestOverlayConfigPathsIncludesMissingConfig(t *testing.T) {
	home := t.TempDir()
	workspace := t.TempDir()
	t.Setenv(config.EnvHome, home)
	writeProfileConfig(t, home, "plugins-only", workspace, "")
	p, found, err := config.LoadProfile("plugins-only")
	if err != nil || !found {
		t.Fatalf("LoadProfile: found=%v err=%v", found, err)
	}
	if p.HasConfig() {
		t.Fatal("expected HasConfig=false")
	}
	paths := p.OverlayConfigPaths(nil)
	if len(paths) != 1 {
		t.Fatalf("paths=%v", paths)
	}
	if !strings.HasSuffix(paths[0], filepath.Join("workspaces", "plugins-only", "config.yaml")) {
		t.Fatalf("want profile overlay path, got %q", paths[0])
	}
	again := p.OverlayConfigPaths(paths)
	if len(again) != 1 || again[0] != paths[0] {
		t.Fatalf("duplicate overlay: %v", again)
	}
}
