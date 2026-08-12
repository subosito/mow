package acp

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/subosito/mow"
)

func TestProfileAgentsAreScopedPerEngineWithoutCapabilityEscalation(t *testing.T) {
	// Phase 2 contract: profile registration must be Engine-owned rather than
	// accumulating in the package-global delegate. Reset test state so this
	// test exposes both cross-profile names and captured first-workspace state.
	sharedMu.Lock()
	sharedDelegate = nil
	sharedMu.Unlock()
	t.Cleanup(func() {
		sharedMu.Lock()
		sharedDelegate = nil
		sharedMu.Unlock()
	})

	home := t.TempDir()
	t.Setenv("MOW_HOME", home)
	workspaceA := t.TempDir()
	workspaceB := t.TempDir()
	writeProfile := func(name, workspace, agent string, allowWrite, allowShell bool) {
		t.Helper()
		dir := filepath.Join(home, "workspaces", name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "workspace.yaml"), []byte("root: "+workspace+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		config := "extensions:\n  acp:\n    mow_agents:\n      " + agent + ":\n        model: gpt-5-mini\n        allow_write: "
		if allowWrite {
			config += "true\n"
		} else {
			config += "false\n"
		}
		config += "        allow_shell: "
		if allowShell {
			config += "true\n"
		} else {
			config += "false\n"
		}
		if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(config), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writeProfile("one", workspaceA, "peer-one", false, false)
	writeProfile("two", workspaceB, "peer-two", true, true)

	var firstTools []mow.ToolSpec
	chatOne := func(_ context.Context, _ []mow.Message, tools []mow.ToolSpec) (mow.Message, error) {
		firstTools = append([]mow.ToolSpec(nil), tools...)
		return mow.Message{Role: "assistant", Content: "ok"}, nil
	}
	first, err := mow.New(mow.Options{LoadUserConfig: true, Workspace: "one", Model: "gpt-5-mini", NoSession: true, Chat: chatOne})
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	if err := RegisterFromEngine(first); err != nil {
		t.Fatal(err)
	}
	if _, err := first.Prompt(context.Background(), "inspect"); err != nil {
		t.Fatal(err)
	}
	if !hasTool(firstTools, "acp_delegate") {
		t.Fatal("first profile missing engine-scoped acp_delegate")
	}

	var secondTools []mow.ToolSpec
	chatTwo := func(_ context.Context, _ []mow.Message, tools []mow.ToolSpec) (mow.Message, error) {
		secondTools = append([]mow.ToolSpec(nil), tools...)
		return mow.Message{Role: "assistant", Content: "ok"}, nil
	}
	second, err := mow.New(mow.Options{LoadUserConfig: true, Workspace: "two", Model: "gpt-5-mini", NoSession: true, Chat: chatTwo})
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	if err := RegisterFromEngine(second); err != nil {
		t.Fatal(err)
	}
	if _, err := second.Prompt(context.Background(), "inspect"); err != nil {
		t.Fatal(err)
	}
	if !hasTool(secondTools, "acp_delegate") {
		t.Fatal("second profile missing engine-scoped acp_delegate")
	}
}

func contains(args []string, want string) bool {
	for _, arg := range args {
		if arg == want {
			return true
		}
	}
	return false
}

func hasTool(specs []mow.ToolSpec, name string) bool {
	for _, spec := range specs {
		if spec.Function.Name == name {
			return true
		}
	}
	return false
}
