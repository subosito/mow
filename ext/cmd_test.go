package ext_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/subosito/mow/ext"
)

func TestRegisterCommandLookup(t *testing.T) {
	ext.Reset()
	t.Cleanup(ext.Reset)

	ext.RegisterCommand(ext.Command{
		Name:    "demo",
		Summary: "demo cmd",
		Run:     func(args []string) int { return 7 },
	})
	c, ok := ext.LookupCommand("demo")
	if !ok || c.Summary != "demo cmd" {
		t.Fatalf("%+v", c)
	}
	if code := c.Run(nil); code != 7 {
		t.Fatalf("code=%d", code)
	}
	if _, ok := ext.LookupCommand("missing"); ok {
		t.Fatal("expected miss")
	}
}

func TestDefaultInteractive(t *testing.T) {
	ext.Reset()
	t.Cleanup(ext.Reset)

	ext.RegisterCommand(ext.Command{Name: "a", Run: func([]string) int { return 0 }})
	ext.RegisterCommand(ext.Command{Name: "b", DefaultInteractive: true, Run: func([]string) int { return 0 }})
	c, ok := ext.DefaultInteractiveCommand()
	if !ok || c.Name != "b" {
		t.Fatalf("%+v", c)
	}
}

func TestCommandLayer(t *testing.T) {
	ext.Reset()
	t.Cleanup(ext.Reset)

	ext.RegisterCommand(ext.Command{Name: "acp", Layer: "ext", Run: func([]string) int { return 0 }})
	ext.RegisterCommand(ext.Command{Name: "goal", Layer: "pack", Run: func([]string) int { return 0 }})
	var layers []string
	for _, c := range ext.Commands() {
		layers = append(layers, c.Name+":"+c.Layer)
	}
	got := fmt.Sprintf("%v", layers)
	if !containsAll(got, "acp:ext", "goal:pack") {
		t.Fatalf("layers=%s", got)
	}
}

func containsAll(s string, parts ...string) bool {
	for _, p := range parts {
		if !strings.Contains(s, p) {
			return false
		}
	}
	return true
}

func TestBeforeNew(t *testing.T) {
	ext.Reset()
	t.Cleanup(ext.Reset)

	var saw []string
	ext.RegisterBeforeNew(func(paths ...string) error {
		saw = append(saw, paths...)
		return nil
	})
	if err := ext.BeforeNew("a.yaml"); err != nil {
		t.Fatal(err)
	}
	if len(saw) != 1 || saw[0] != "a.yaml" {
		t.Fatalf("%v", saw)
	}
}

func TestHookSourceClearAndHermeticFilter(t *testing.T) {
	ext.Reset()
	t.Cleanup(ext.Reset)

	// Static hook (outside BeforeNew).
	ext.RegisterPreTool(func(ctx context.Context, e ext.PreToolEvent) (ext.PreToolDecision, error) {
		return ext.PreToolDecision{}, nil
	})
	// Config-sourced hooks during BeforeNew.
	ext.RegisterBeforeNew(func(paths ...string) error {
		ext.ClearHookSource("cmdhook")
		ext.RegisterPreToolSource("cmdhook", func(ctx context.Context, e ext.PreToolEvent) (ext.PreToolDecision, error) {
			return ext.PreToolDecision{Deny: true, Message: "cfg"}, nil
		})
		return nil
	})
	if err := ext.BeforeNew("x.yaml"); err != nil {
		t.Fatal(err)
	}
	if n := len(ext.PreToolHooks()); n != 2 {
		t.Fatalf("all hooks=%d want 2", n)
	}
	// Hermetic: static + current gen only (both present).
	if n := len(ext.PreToolHooksForEngine(false)); n != 2 {
		t.Fatalf("hermetic hooks=%d want 2", n)
	}
	// Second BeforeNew replaces cmdhook source, does not accumulate.
	if err := ext.BeforeNew("y.yaml"); err != nil {
		t.Fatal(err)
	}
	if n := len(ext.PreToolHooks()); n != 2 {
		t.Fatalf("after replace all=%d want 2 (static+one cmdhook)", n)
	}
	ext.ClearHookSource("cmdhook")
	if n := len(ext.PreToolHooks()); n != 1 {
		t.Fatalf("after clear cmdhook=%d want 1 static", n)
	}
}

func TestClearHookSourceClearsAfterTurnDecide(t *testing.T) {
	ext.Reset()
	t.Cleanup(ext.Reset)
	ext.RegisterAfterTurnDecisionSource("focus", func(ctx context.Context, e ext.AfterTurnEvent) (ext.AfterTurnDecision, error) {
		return ext.AfterTurnDecision{}, nil
	})
	if n := len(ext.AfterTurnDecisionHooks()); n != 1 {
		t.Fatalf("before clear=%d want 1", n)
	}
	ext.ClearHookSource("focus")
	if n := len(ext.AfterTurnDecisionHooks()); n != 0 {
		t.Fatalf("after clear=%d want 0", n)
	}
}

func TestBeforeNewGenerationRelease(t *testing.T) {
	ext.Reset()
	t.Cleanup(ext.Reset)

	var released []int
	ext.RegisterGenerationRelease(func(gen int) {
		released = append(released, gen)
	})
	ext.RegisterBeforeNew(func(...string) error { return nil })
	if err := ext.BeforeNew(); err != nil {
		t.Fatal(err)
	}
	gen := ext.BeforeNewGeneration()
	ext.NoteEngineGeneration(gen)
	ext.ReleaseEngineGeneration(gen)
	if len(released) != 1 || released[0] != gen {
		t.Fatalf("released=%v want [%d]", released, gen)
	}
	// Extra release without a matching NoteEngineGeneration is a no-op.
	ext.ReleaseEngineGeneration(gen)
	if len(released) != 1 {
		t.Fatalf("expected single release, got %v", released)
	}
}

func TestGenerationEngineRefs(t *testing.T) {
	ext.Reset()
	t.Cleanup(ext.Reset)

	if ext.GenerationEngineRefs(1) != 0 {
		t.Fatal("expected zero refs for unknown gen")
	}
	ext.RegisterBeforeNew(func(...string) error { return nil })
	if err := ext.BeforeNew(); err != nil {
		t.Fatal(err)
	}
	gen := ext.BeforeNewGeneration()
	ext.NoteEngineGeneration(gen)
	if ext.GenerationEngineRefs(gen) != 1 {
		t.Fatalf("refs=%d want 1", ext.GenerationEngineRefs(gen))
	}
	ext.ReleaseEngineGeneration(gen)
	if ext.GenerationEngineRefs(gen) != 0 {
		t.Fatalf("refs after release=%d want 0", ext.GenerationEngineRefs(gen))
	}
}

func TestBeginSetupFailureRollsBackGeneration(t *testing.T) {
	ext.Reset()
	t.Cleanup(ext.Reset)

	ext.RegisterExtensionInstance("mcp", "existing", 2)
	var released []int
	ext.RegisterGenerationRelease(func(gen int) {
		released = append(released, gen)
	})
	ext.RegisterBeforeNew(func(...string) error {
		ext.RegisterTool(fakeTool("failed-tool"))
		ext.RegisterPreTool(func(context.Context, ext.PreToolEvent) (ext.PreToolDecision, error) {
			return ext.PreToolDecision{Deny: true}, nil
		})
		ext.RegisterExtensionInstance("mcp", "partial", 0)
		return nil
	})
	ext.RegisterBeforeNew(func(...string) error { return fmt.Errorf("setup failed") })

	if err := ext.BeforeNew(); err == nil || !strings.Contains(err.Error(), "setup failed") {
		t.Fatalf("BeforeNew error = %v", err)
	}
	gen := ext.BeforeNewGeneration()
	if len(released) != 1 || released[0] != gen {
		t.Fatalf("released = %v, want [%d]", released, gen)
	}
	for _, tool := range ext.Tools() {
		if tool.Name() == "failed-tool" {
			t.Fatal("failed-generation tool remained registered")
		}
	}
	for _, tool := range ext.ToolsForEngine(true) {
		if tool.Name() == "failed-tool" {
			t.Fatal("failed-generation tool leaked into a later host snapshot")
		}
	}
	if n := len(ext.PreToolHooks()); n != 0 {
		t.Fatalf("failed-generation hooks = %d, want 0", n)
	}
	instances := ext.ListExtensions(0)
	if len(instances) != 1 || instances[0].Name != "mcp:existing" || instances[0].MinTurns != 2 {
		t.Fatalf("extension instances after rollback = %+v", instances)
	}
}

type fakeTool string

func (f fakeTool) Name() string                                        { return string(f) }
func (fakeTool) Description() string                                   { return "test" }
func (fakeTool) Parameters() json.RawMessage                           { return json.RawMessage(`{"type":"object"}`) }
func (fakeTool) Exec(context.Context, json.RawMessage) (string, error) { return "", nil }

func TestResetPreservesGenerationRelease(t *testing.T) {
	ext.Reset()
	t.Cleanup(ext.Reset)

	var released []int
	ext.RegisterGenerationRelease(func(gen int) {
		released = append(released, gen)
	})
	ext.RegisterBeforeNew(func(...string) error { return nil })
	if err := ext.BeforeNew(); err != nil {
		t.Fatal(err)
	}
	gen := ext.BeforeNewGeneration()
	ext.NoteEngineGeneration(gen)
	ext.Reset()
	ext.RegisterBeforeNew(func(...string) error { return nil })
	if err := ext.BeforeNew(); err != nil {
		t.Fatal(err)
	}
	gen2 := ext.BeforeNewGeneration()
	ext.NoteEngineGeneration(gen2)
	ext.ReleaseEngineGeneration(gen2)
	if len(released) != 1 || released[0] != gen2 {
		t.Fatalf("released=%v want [%d]", released, gen2)
	}
}

func TestResetClearsAfterTurnDecisionHooks(t *testing.T) {
	ext.Reset()
	t.Cleanup(ext.Reset)
	ext.RegisterAfterTurnDecision(func(context.Context, ext.AfterTurnEvent) (ext.AfterTurnDecision, error) {
		return ext.AfterTurnDecision{}, nil
	})
	if n := len(ext.AfterTurnDecisionHooks()); n != 1 {
		t.Fatalf("hooks=%d, want 1", n)
	}
	ext.Reset()
	if n := len(ext.AfterTurnDecisionHooks()); n != 0 {
		t.Fatalf("Reset left %d after-turn decision hook(s)", n)
	}
}
