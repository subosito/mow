package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/subosito/mow/internal/config"
)

func TestHomeMOW_HOME(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(config.EnvHome, dir)
	got := config.Home()
	want, err := filepath.Abs(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("Home()=%q want %q", got, want)
	}
	if config.ConfigPath() != filepath.Join(want, "config.yaml") {
		t.Fatalf("ConfigPath=%q", config.ConfigPath())
	}
	if config.SessionsDir() != filepath.Join(want, "sessions") {
		t.Fatalf("SessionsDir=%q", config.SessionsDir())
	}
	if config.SkillsDir() != filepath.Join(want, "skills") {
		t.Fatalf("SkillsDir=%q", config.SkillsDir())
	}
	if config.AgentsPath() != filepath.Join(want, "AGENTS.md") {
		t.Fatalf("AgentsPath=%q", config.AgentsPath())
	}
}

func TestHomeTildeExpand(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home")
	}
	t.Setenv(config.EnvHome, "~/mow-test-home")
	got := config.Home()
	want := filepath.Join(home, "mow-test-home")
	if abs, err := filepath.Abs(want); err == nil {
		want = abs
	}
	if got != want {
		t.Fatalf("Home()=%q want %q", got, want)
	}
}

func TestHomeDefault(t *testing.T) {
	t.Setenv(config.EnvHome, "")
	// Empty MOW_HOME → ~/.mow (not ".")
	got := config.Home()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home")
	}
	want := filepath.Join(home, ".mow")
	if got != want {
		t.Fatalf("Home()=%q want %q", got, want)
	}
}

func TestLoadUsesMOW_HOMENotUserDotMow(t *testing.T) {
	// Developer ~/.mow often enables write/bash — tests must not see it.
	iso := t.TempDir()
	t.Setenv(config.EnvHome, iso)
	// Also write a "polluting" style config under real home? We can't safely;
	// instead put a config under iso and prove it's merged, and defaults stay secure
	// when no config exists.
	t.Setenv("OPENAI_API_KEY", "sk-test")
	t.Setenv("OPENAI_MODEL", "m")
	t.Setenv("OPENAI_BASE_URL", "http://example.com/v1")

	f, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if f.ToolEnabled("bash") || f.ToolEnabled("write") {
		t.Fatalf("power tools should be off with empty MOW_HOME: %v", f.Tools.Enable)
	}
	if f.Session.Dir != config.SessionsDir() {
		t.Fatalf("session.dir=%q want %q", f.Session.Dir, config.SessionsDir())
	}

	// Config under MOW_HOME is loaded
	cfgPath := config.ConfigPath()
	if err := os.WriteFile(cfgPath, []byte("tools:\n  enable:\n    - read\n    - write\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f2, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if !f2.ToolEnabled("write") || f2.ToolEnabled("bash") {
		t.Fatalf("MOW_HOME config not applied: %v", f2.Tools.Enable)
	}
}
