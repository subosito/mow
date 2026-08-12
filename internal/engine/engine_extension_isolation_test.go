package engine_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/subosito/mow"
	"github.com/subosito/mow/ext"
)

func TestNewSkipExtensionSetupSkipsBeforeNewAndExtTools(t *testing.T) {
	ext.Reset()
	t.Cleanup(ext.Reset)

	var beforeNewRan bool
	ext.RegisterBeforeNew(func(configPaths ...string) error {
		beforeNewRan = true
		return nil
	})
	ext.RegisterTool(extIsolationTool{name: "poison_ext"})

	var specsSeen []string
	eng, err := mow.New(mow.Options{
		LoadUserConfig:     true,
		SkipExtensionSetup: true,
		NoSession:          true,
		Chat: func(ctx context.Context, messages []mow.Message, tools []mow.ToolSpec) (mow.Message, error) {
			specsSeen = nil
			for _, ts := range tools {
				specsSeen = append(specsSeen, ts.Function.Name)
			}
			return mow.Message{Role: "assistant", Content: "ok"}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()

	if beforeNewRan {
		t.Fatal("ext.BeforeNew ran with SkipExtensionSetup")
	}
	if _, err := eng.Prompt(t.Context(), "hi"); err != nil {
		t.Fatal(err)
	}
	for _, name := range specsSeen {
		if name == "poison_ext" {
			t.Fatalf("extension tool %q merged despite SkipExtensionSetup", name)
		}
	}
}

func TestNewDefaultRunsBeforeNewAndExtTools(t *testing.T) {
	ext.Reset()
	t.Cleanup(ext.Reset)

	home := t.TempDir()
	cfgPath := filepath.Join(home, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte("tools:\n  enable: [read, glob, grep]\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	var beforeNewRan bool
	ext.RegisterBeforeNew(func(configPaths ...string) error {
		beforeNewRan = true
		return nil
	})
	ext.RegisterTool(extIsolationTool{name: "visible_ext"})

	var specsSeen []string
	var chatCalls int
	eng, err := mow.New(mow.Options{
		ConfigPaths: []string{cfgPath},
		Model:       "test-model",
		NoSession:   true,
		Chat: func(ctx context.Context, messages []mow.Message, tools []mow.ToolSpec) (mow.Message, error) {
			chatCalls++
			specsSeen = nil
			for _, ts := range tools {
				specsSeen = append(specsSeen, ts.Function.Name)
			}
			return mow.Message{Role: "assistant", Content: "ok"}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()

	if !beforeNewRan {
		t.Fatal("ext.BeforeNew did not run for default engine")
	}
	if _, err := eng.Prompt(t.Context(), "hello"); err != nil {
		t.Fatal(err)
	}
	if chatCalls == 0 {
		t.Fatal("chat was never called")
	}
	if len(ext.Tools()) != 1 {
		t.Fatalf("ext.Tools() = %d want 1", len(ext.Tools()))
	}
	found := false
	for _, name := range specsSeen {
		if name == "visible_ext" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("extension tool not merged: specs=%v", specsSeen)
	}
}

func TestNewDisableExtensionHooksSkipsGlobalHooks(t *testing.T) {
	ext.Reset()
	t.Cleanup(ext.Reset)

	ext.RegisterUserPrompt(func(ctx context.Context, e ext.UserPromptEvent) (ext.UserPromptDecision, error) {
		return ext.UserPromptDecision{RewriteText: true, Text: "ext:" + e.Text}, nil
	})
	ext.RegisterPreTool(func(ctx context.Context, e ext.PreToolEvent) (ext.PreToolDecision, error) {
		return ext.PreToolDecision{Deny: true, Message: "ext-pre"}, nil
	})
	ext.RegisterPostTool(func(ctx context.Context, e ext.PostToolEvent) (ext.PostToolDecision, error) {
		return ext.PostToolDecision{Rewrite: true, Result: "ext-post"}, nil
	})
	ext.RegisterSessionStart(func(ctx context.Context, e ext.SessionStartEvent) (ext.SessionStartDecision, error) {
		return ext.SessionStartDecision{SystemAppend: "EXT_SESSION_START"}, nil
	})
	ext.RegisterTool(extIsolationTool{name: "echo_ext"})

	step := 0
	chat := func(ctx context.Context, messages []mow.Message, tools []mow.ToolSpec) (mow.Message, error) {
		step++
		if step == 1 {
			return mow.Message{
				Role: "assistant",
				ToolCalls: []mow.ToolCall{{
					ID: "1", Type: "function",
					Function: mow.FunctionCall{Name: "echo_ext", Arguments: `{}`},
				}},
			}, nil
		}
		for _, m := range messages {
			if m.Role == "user" && strings.HasPrefix(m.Content, "ext:") {
				t.Fatalf("ext user prompt hook ran: %q", m.Content)
			}
			if m.Role == "system" && strings.Contains(m.Content, "EXT_SESSION_START") {
				t.Fatal("ext session start hook ran")
			}
			if m.Role == "tool" && strings.Contains(m.Content, "ext-pre") {
				t.Fatal("ext pre-tool hook ran")
			}
			if m.Role == "tool" && strings.Contains(m.Content, "ext-post") {
				t.Fatal("ext post-tool hook ran")
			}
		}
		return mow.Message{Role: "assistant", Content: "ok"}, nil
	}

	eng, err := mow.New(mow.Options{
		DisableExtensionHooks: true,
		NoSession:             true,
		Chat:                  chat,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()

	if _, err := eng.Prompt(t.Context(), "hello"); err != nil {
		t.Fatal(err)
	}
}

func TestNewDisableExtensionHooksPreservesOptionsHooks(t *testing.T) {
	ext.Reset()
	t.Cleanup(ext.Reset)

	ext.RegisterUserPrompt(func(ctx context.Context, e ext.UserPromptEvent) (ext.UserPromptDecision, error) {
		return ext.UserPromptDecision{RewriteText: true, Text: "ext:" + e.Text}, nil
	})

	var sawOpt bool
	chat := func(ctx context.Context, messages []mow.Message, tools []mow.ToolSpec) (mow.Message, error) {
		for _, m := range messages {
			if m.Role == "user" && strings.HasPrefix(m.Content, "opt:") {
				sawOpt = true
			}
			if m.Role == "user" && strings.HasPrefix(m.Content, "ext:") {
				t.Fatalf("ext hook ran with DisableExtensionHooks: %q", m.Content)
			}
		}
		return mow.Message{Role: "assistant", Content: "done"}, nil
	}

	eng, err := mow.New(mow.Options{
		DisableExtensionHooks: true,
		NoSession:             true,
		Chat:                  chat,
		Hooks: mow.Hooks{
			OnUserPrompt: []mow.UserPromptFunc{
				func(ctx context.Context, e mow.UserPromptEvent) (mow.UserPromptDecision, error) {
					return mow.UserPromptDecision{RewriteText: true, Text: "opt:" + e.Text}, nil
				},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()

	if _, err := eng.Prompt(t.Context(), "hello"); err != nil {
		t.Fatal(err)
	}
	if !sawOpt {
		t.Fatal("Options.OnUserPrompt did not run")
	}
}

type extIsolationTool struct {
	name string
}

func (t extIsolationTool) Name() string { return t.name }

func (extIsolationTool) Description() string { return "test ext tool" }

func (extIsolationTool) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object"}`)
}

func (extIsolationTool) Exec(context.Context, json.RawMessage) (string, error) {
	return "ran", nil
}
