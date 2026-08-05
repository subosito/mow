package cmdhook

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/subosito/mow/ext"
)

func TestMultiPluginConfigAndMinTurns(t *testing.T) {
	dir1 := t.TempDir()
	dir2 := t.TempDir()

	hooksDir1 := filepath.Join(dir1, "hooks")
	if err := os.MkdirAll(hooksDir1, 0o755); err != nil {
		t.Fatal(err)
	}
	hooksDir2 := filepath.Join(dir2, "hooks")
	if err := os.MkdirAll(hooksDir2, 0o755); err != nil {
		t.Fatal(err)
	}

	hooks1 := `{"hooks": {"UserPromptSubmit": [{"hooks": [{"type": "command", "command": "echo p1"}]}]}}`
	if err := os.WriteFile(filepath.Join(hooksDir1, "hooks.json"), []byte(hooks1), 0o644); err != nil {
		t.Fatal(err)
	}
	hooks2 := `{"hooks": {"UserPromptSubmit": [{"hooks": [{"type": "command", "command": "echo p2"}]}]}}`
	if err := os.WriteFile(filepath.Join(hooksDir2, "hooks.json"), []byte(hooks2), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := Config{
		Plugins: map[string]PluginConfig{
			"p1": {Root: dir1, MinTurns: 0},
			"p2": {Root: dir2, MinTurns: 3},
		},
	}

	plugins := cfg.resolved()
	if len(plugins) != 2 {
		t.Fatalf("expected 2 plugins, got %d", len(plugins))
	}

	ext.Reset()
	for _, p := range plugins {
		b, err := load(p)
		if err != nil {
			t.Fatalf("load failed for %s: %v", p.Name, err)
		}
		if b == nil {
			t.Fatalf("nil bridge for %s", p.Name)
		}
		b.register()
	}

	// At turn 1: p1 active, p2 dormant
	ctx1 := ext.WithTurn(context.Background(), 1)
	if !ext.IsExtensionActive("cmdhook:p1", 1) {
		t.Error("p1 should be active at turn 1")
	}
	if ext.IsExtensionActive("cmdhook:p2", 1) {
		t.Error("p2 should be dormant at turn 1")
	}

	// At turn 3: both active
	ctx3 := ext.WithTurn(context.Background(), 3)
	if !ext.IsExtensionActive("cmdhook:p2", 3) {
		t.Error("p2 should be active at turn 3")
	}

	// Test user prompt hook execution at turn 1 vs turn 3
	hooks := ext.UserPromptHooks()
	if len(hooks) != 2 {
		t.Fatalf("expected 2 UserPromptHooks, got %d", len(hooks))
	}

	// Turn 1
	d1, err := hooks[0](ctx1, ext.UserPromptEvent{Text: "test"})
	if err != nil || d1.SystemAppend == "" {
		t.Errorf("hook 0 (p1) should execute at turn 1, got append=%q err=%v", d1.SystemAppend, err)
	}
	d2, err := hooks[1](ctx1, ext.UserPromptEvent{Text: "test"})
	if err != nil || d2.SystemAppend != "" {
		t.Errorf("hook 1 (p2) should be dormant at turn 1, got append=%q err=%v", d2.SystemAppend, err)
	}

	// Turn 3
	d2_turn3, err := hooks[1](ctx3, ext.UserPromptEvent{Text: "test"})
	if err != nil || d2_turn3.SystemAppend == "" {
		t.Errorf("hook 1 (p2) should execute at turn 3, got append=%q err=%v", d2_turn3.SystemAppend, err)
	}

	// Toggle p1 off manually
	if !ext.SetExtensionEnabled("p1", false) {
		t.Error("SetExtensionEnabled p1 false failed")
	}
	d1_disabled, _ := hooks[0](ctx3, ext.UserPromptEvent{Text: "test"})
	if d1_disabled.SystemAppend != "" {
		t.Errorf("hook 0 (p1) should be disabled after manual toggle off, got append=%q", d1_disabled.SystemAppend)
	}
}

func TestNormalizeClaudeToolNames(t *testing.T) {
	in := "Use mcp__plugin_context-mode_context-mode__ctx_execute and mcp__context-mode__ctx_search"
	got := normalizeClaudeToolNames(in, "context-mode")
	want := "Use mcp_context-mode_ctx_execute and mcp_context-mode_ctx_search"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
