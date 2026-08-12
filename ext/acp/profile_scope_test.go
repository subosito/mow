package acp

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/subosito/mow"
)

func TestProfileMowAgentCommandRespectsHostCaps(t *testing.T) {
	sharedMu.Lock()
	sharedDelegate = nil
	sharedMu.Unlock()
	t.Cleanup(func() {
		sharedMu.Lock()
		sharedDelegate = nil
		sharedMu.Unlock()
	})

	orig := mowAgentBinary
	mowAgentBinary = func() string { return "mow" }
	t.Cleanup(func() { mowAgentBinary = orig })

	home := t.TempDir()
	workspace := t.TempDir()
	extra := t.TempDir()
	t.Setenv("MOW_HOME", home)
	t.Setenv("OPENAI_API_KEY", "k")
	t.Setenv("OPENAI_MODEL", "m")

	dir := filepath.Join(home, "workspaces", "cap-test")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "workspace.yaml"), []byte("root: "+workspace+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	body := "extensions:\n  acp:\n    mow_agents:\n      coder:\n        model: gpt-5-mini\n        allow_write: true\n        allow_shell: true\n"
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	eng, err := mow.New(mow.Options{
		LoadUserConfig: true,
		Workspace:      "cap-test",
		Model:          "gpt-5-mini",
		NoSession:      true,
		ExtraRoots:     []string{extra},
		Chat:           stubChat,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()

	tool := captureDelegateViaEngineConfig(t, eng)
	spec := tool.agents["coder"]
	if spec.Mow == nil {
		t.Fatal("expected native mow agent")
	}
	host := hostPolicyFromContext(mow.ContextWithEngine(context.Background(), eng), workspace)
	cmd := peerCommand(spec, host, workspace)
	joined := strings.Join(cmd, " ")
	if strings.Contains(joined, "--allow-write") || strings.Contains(joined, "--allow-shell") {
		t.Fatalf("read-only host must cap profile allow flags: %v", cmd)
	}
	if !strings.Contains(joined, "--read-only") {
		t.Fatalf("expected inherited read-only: %v", cmd)
	}
	if !strings.Contains(joined, "--extra-root "+extra) {
		t.Fatalf("expected host extra root: %v", cmd)
	}
}

func TestProfileAgentsAreScopedPerEngineWithoutCapabilityEscalation(t *testing.T) {
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

	orig := mowAgentBinary
	mowAgentBinary = func() string { return "mow" }
	t.Cleanup(func() { mowAgentBinary = orig })

	var firstTools []mow.ToolSpec
	first, err := mow.New(mow.Options{
		LoadUserConfig: true,
		Workspace:      "one",
		Model:          "gpt-5-mini",
		NoSession:      true,
		Chat: func(_ context.Context, _ []mow.Message, tools []mow.ToolSpec) (mow.Message, error) {
			firstTools = append([]mow.ToolSpec(nil), tools...)
			return mow.Message{Role: "assistant", Content: "ok"}, nil
		},
	})
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
	toolOne := captureDelegateViaEngineConfig(t, first)
	hostOne := hostPolicyFromContext(mow.ContextWithEngine(context.Background(), first), workspaceA)
	cmdOne := peerCommand(toolOne.agents["peer-one"], hostOne, workspaceA)
	if strings.Contains(strings.Join(cmdOne, " "), "--allow-write") {
		t.Fatalf("profile one must not escalate write: %v", cmdOne)
	}

	second, err := mow.New(mow.Options{
		LoadUserConfig: true,
		Workspace:      "two",
		Model:          "gpt-5-mini",
		NoSession:      true,
		AllowWrite:     true,
		AllowShell:     true,
		Chat:           stubChat,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	if err := RegisterFromEngine(second); err != nil {
		t.Fatal(err)
	}
	toolTwo := captureDelegateViaEngineConfig(t, second)
	hostTwo := hostPolicyFromContext(mow.ContextWithEngine(context.Background(), second), workspaceB)
	cmdTwo := peerCommand(toolTwo.agents["peer-two"], hostTwo, workspaceB)
	joined := strings.Join(cmdTwo, " ")
	if !strings.Contains(joined, "--allow-write") || !strings.Contains(joined, "--allow-shell") {
		t.Fatalf("profile two with powered host should allow caps: %v", cmdTwo)
	}
	if !hasTool(firstTools, "acp_delegate") {
		t.Fatal("first profile missing acp_delegate in tool list")
	}
}

func captureDelegateViaEngineConfig(t *testing.T, eng *mow.Engine) *delegateTool {
	t.Helper()
	var c Config
	if err := eng.Extension("acp", &c); err != nil {
		t.Fatal(err)
	}
	agents, err := resolveAgents(c)
	if err != nil {
		t.Fatal(err)
	}
	return &delegateTool{
		agents:    indexAgents(agents),
		workspace: eng.Workspace(),
		peerIdle:  peerIdleDuration(c.PeerIdleSec),
		peers:     map[string]*peerSlot{},
	}
}

func stubChat(context.Context, []mow.Message, []mow.ToolSpec) (mow.Message, error) {
	return mow.Message{Role: "assistant", Content: "ok"}, nil
}

func hasTool(specs []mow.ToolSpec, name string) bool {
	for _, spec := range specs {
		if spec.Function.Name == name {
			return true
		}
	}
	return false
}
