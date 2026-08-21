package engine

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNewWarnsUnregisteredEnable(t *testing.T) {
	home := t.TempDir()
	t.Setenv("MOW_HOME", home)
	t.Setenv("OPENAI_API_KEY", "k")
	t.Setenv("MOW_API_KEY", "")
	t.Setenv("ANTHROPIC_API_KEY", "")
	cfg := filepath.Join(home, "config.yaml")
	body := "llm:\n  model: m\ntools:\n  enable: [read, glob, grep, understand_image]\n"
	if err := os.WriteFile(cfg, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn}))
	eng, err := New(Options{
		DeferLLM:       true,
		NoSession:      true,
		LoadUserConfig: true,
		ConfigPaths:    []string{cfg},
		Logger:         logger,
		Workspace:      t.TempDir(),
	})
	if err != nil {
		t.Fatalf("New must succeed: %v", err)
	}
	defer eng.Close()
	out := buf.String()
	if !strings.Contains(out, "understand_image") || !strings.Contains(out, "not registered") {
		t.Fatalf("want warn for understand_image; log=%q", out)
	}
	if !strings.Contains(out, "packs/media") || !strings.Contains(out, "mow-full") {
		t.Fatalf("want lean/mow-full hint; log=%q", out)
	}
	for _, tl := range eng.tools {
		if tl.Name() == "understand_image" {
			t.Fatal("understand_image must stay absent")
		}
	}
}
