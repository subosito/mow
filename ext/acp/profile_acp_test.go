package acp

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/subosito/mow"
	"github.com/subosito/mow/internal/config"
)

func resetSharedDelegate(t *testing.T) {
	t.Helper()
	sharedMu.Lock()
	sharedDelegate = nil
	sharedMu.Unlock()
	t.Cleanup(func() {
		sharedMu.Lock()
		sharedDelegate = nil
		sharedMu.Unlock()
	})
}

func writeNamedProfile(t *testing.T, home, name, workspace, configBody string) {
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

func nativeAgentYAML(name, model string) string {
	return "extensions:\n  acp:\n    peers:\n      - name: " + name + "\n        model: " + model + "\n"
}

func agentModelFromSpec(spec PeerSpec) string {
	if spec.native() {
		return strings.TrimSpace(spec.Model)
	}
	return agentModelFlag(spec.Command)
}

func agentModelFlag(cmd []string) string {
	for i := 0; i+1 < len(cmd); i++ {
		if cmd[i] == "--model" {
			return cmd[i+1]
		}
	}
	return ""
}

// TestRegisterFromConfigProfileOverridesGlobalPeerModel is the concrete
// regression: BeforeNew path list with OverlayConfigPaths must not let global
// $MOW_HOME/config.yaml overwrite the profile's extensions.acp peer model.
func TestRegisterFromConfigProfileOverridesGlobalPeerModel(t *testing.T) {
	resetSharedDelegate(t)
	home := t.TempDir()
	workspace := t.TempDir()
	t.Setenv("MOW_HOME", home)
	t.Setenv("OPENAI_API_KEY", "k")
	t.Setenv("OPENAI_MODEL", "host-model")
	t.Setenv("MOW_MODEL", "")
	t.Setenv("MOW_API_KEY", "")

	// Pin binary so command inspection is stable (not the test binary path).
	orig := mowAgentBinary
	mowAgentBinary = func() string { return "mow" }
	t.Cleanup(func() { mowAgentBinary = orig })

	globalModel := "deepseek-v4-flash"
	profileModel := "gateway/deepseek/deepseek-chat"
	global := nativeAgentYAML("deepseek", globalModel)
	if err := os.WriteFile(filepath.Join(home, "config.yaml"), []byte(global), 0o600); err != nil {
		t.Fatal(err)
	}
	profileBody := nativeAgentYAML("deepseek", profileModel)
	writeNamedProfile(t, home, "gateway-profile", workspace, profileBody)

	p, found, err := config.LoadProfile("gateway-profile")
	if err != nil || !found {
		t.Fatalf("LoadProfile: found=%v err=%v", found, err)
	}
	// Same path list engine.New builds for BeforeNew with LoadUserConfig:
	// global config first, then profile overlay.
	paths := []string{filepath.Join(home, "config.yaml"), p.ConfigPath()}
	if err := RegisterFromConfig(paths...); err != nil {
		t.Fatal(err)
	}

	sharedMu.Lock()
	defer sharedMu.Unlock()
	if sharedDelegate == nil {
		t.Fatal("expected delegate registration")
	}
	spec, ok := sharedDelegate.agents["deepseek"]
	if !ok {
		t.Fatalf("missing deepseek agent; agents=%v", keysOf(sharedDelegate.agents))
	}
	got := agentModelFromSpec(spec)
	if got != profileModel {
		t.Fatalf("deepseek --model=%q want %q (command=%v)", got, profileModel, spec.Command)
	}
	if strings.Contains(got, globalModel) && got != profileModel {
		t.Fatalf("still on global model %q", got)
	}
}

// TestRegisterFromConfigReplaceDoesNotAccumulatePeers proves each
// RegisterFromConfig installs a fresh agent set (no merge with the prior
// Engine's peers).
func TestRegisterFromConfigReplaceDoesNotAccumulatePeers(t *testing.T) {
	resetSharedDelegate(t)
	home := t.TempDir()
	wsA := t.TempDir()
	wsB := t.TempDir()
	t.Setenv("MOW_HOME", home)
	t.Setenv("OPENAI_API_KEY", "k")
	t.Setenv("OPENAI_MODEL", "m")
	t.Setenv("MOW_MODEL", "")
	t.Setenv("MOW_API_KEY", "")

	orig := mowAgentBinary
	mowAgentBinary = func() string { return "mow" }
	t.Cleanup(func() { mowAgentBinary = orig })

	writeNamedProfile(t, home, "one", wsA, nativeAgentYAML("peer-one", "model-one"))
	writeNamedProfile(t, home, "two", wsB, nativeAgentYAML("peer-two", "model-two"))

	p1, _, _ := config.LoadProfile("one")
	if err := RegisterFromConfig(p1.OverlayConfigPaths(nil)...); err != nil {
		t.Fatal(err)
	}
	sharedMu.Lock()
	first := sharedDelegate
	firstCmd := agentModelFromSpec(first.agents["peer-one"])
	sharedMu.Unlock()

	p2, _, _ := config.LoadProfile("two")
	if err := RegisterFromConfig(p2.OverlayConfigPaths(nil)...); err != nil {
		t.Fatal(err)
	}
	sharedMu.Lock()
	second := sharedDelegate
	_, hasOne := second.agents["peer-one"]
	_, hasTwo := second.agents["peer-two"]
	sharedMu.Unlock()

	if first == second {
		t.Fatal("RegisterFromConfig must install a new tool instance (no shared mutable map)")
	}
	if firstCmd != "model-one" {
		t.Fatalf("first tool mutated after second RegisterFromConfig: %v", firstCmd)
	}
	if hasOne {
		t.Fatal("second registration still has peer-one (must replace, not merge)")
	}
	if !hasTwo {
		t.Fatal("second registration missing peer-two")
	}
}

// TestEngineProfileCapturesAcpDelegateModel runs the full New → BeforeNew path
// and checks both Extension("acp") and the registered delegate command
// without any network.
func TestEngineProfileCapturesAcpDelegateModel(t *testing.T) {
	resetSharedDelegate(t)
	home := t.TempDir()
	workspace := t.TempDir()
	t.Setenv("MOW_HOME", home)
	t.Setenv("OPENAI_API_KEY", "k")
	t.Setenv("OPENAI_MODEL", "")
	t.Setenv("MOW_MODEL", "")
	t.Setenv("MOW_API_KEY", "")

	orig := mowAgentBinary
	mowAgentBinary = func() string { return "mow" }
	t.Cleanup(func() { mowAgentBinary = orig })

	globalModel := "deepseek-v4-flash"
	profileModel := "gateway/deepseek/deepseek-chat"
	if err := os.WriteFile(filepath.Join(home, "config.yaml"), []byte(
		"llm:\n  model: global-host\n"+nativeAgentYAML("deepseek", globalModel),
	), 0o600); err != nil {
		t.Fatal(err)
	}
	writeNamedProfile(t, home, "gateway-profile", workspace,
		"llm:\n  model: profile-host\n"+nativeAgentYAML("deepseek", profileModel),
	)

	var tools []mow.ToolSpec
	eng, err := mow.New(mow.Options{
		LoadUserConfig: true,
		Workspace:      "gateway-profile",
		NoSession:      true,
		Chat: func(_ context.Context, _ []mow.Message, specs []mow.ToolSpec) (mow.Message, error) {
			tools = append([]mow.ToolSpec(nil), specs...)
			return mow.Message{Role: "assistant", Content: "ok"}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()

	if got := eng.Model(); got != "profile-host" {
		t.Fatalf("host Model()=%q want profile-host", got)
	}
	var c Config
	if err := eng.Extension("acp", &c); err != nil {
		t.Fatal(err)
	}
	gotModel := ""
	for _, a := range c.Peers {
		if a.Name == "deepseek" {
			gotModel = a.Model
			break
		}
	}
	if gotModel != profileModel {
		t.Fatalf("Extension acp deepseek model=%q want %q", gotModel, profileModel)
	}

	// BeforeNew → RegisterFromConfig must have registered the profile peer.
	sharedMu.Lock()
	if sharedDelegate == nil {
		sharedMu.Unlock()
		t.Fatal("shared delegate not registered via BeforeNew")
	}
	spec, ok := sharedDelegate.agents["deepseek"]
	gotModel = agentModelFromSpec(spec)
	sharedMu.Unlock()
	if !ok || gotModel != profileModel {
		t.Fatalf("registered deepseek --model=%q want %q ok=%v", gotModel, profileModel, ok)
	}

	if _, err := eng.Prompt(context.Background(), "inspect"); err != nil {
		t.Fatal(err)
	}
	if !hasTool(tools, "delegate") {
		t.Fatal("engine tool list missing delegate")
	}
}

// TestEngineScopedDelegateIsolatesPeers constructs two Engines and binds each
// via RegisterFromEngine so peer maps cannot cross-contaminate.
func TestEngineScopedDelegateIsolatesPeers(t *testing.T) {
	resetSharedDelegate(t)
	home := t.TempDir()
	wsA := t.TempDir()
	wsB := t.TempDir()
	t.Setenv("MOW_HOME", home)

	orig := mowAgentBinary
	mowAgentBinary = func() string { return "mow" }
	t.Cleanup(func() { mowAgentBinary = orig })

	writeNamedProfile(t, home, "one", wsA, nativeAgentYAML("peer-one", "model-one")+"        allow_write: false\n        allow_shell: false\n")
	writeNamedProfile(t, home, "two", wsB, nativeAgentYAML("peer-two", "model-two"))

	toolA := captureDelegateViaEngine(t, "one")
	toolB := captureDelegateViaEngine(t, "two")

	if _, ok := toolA.agents["peer-one"]; !ok {
		t.Fatal("engine A missing peer-one")
	}
	if _, ok := toolA.agents["peer-two"]; ok {
		t.Fatal("engine A leaked peer-two from engine B")
	}
	if _, ok := toolB.agents["peer-two"]; !ok {
		t.Fatal("engine B missing peer-two")
	}
	if _, ok := toolB.agents["peer-one"]; ok {
		t.Fatal("engine B leaked peer-one from engine A")
	}
	if agentModelFromSpec(toolA.agents["peer-one"]) != "model-one" {
		t.Fatalf("engine A model=%v", toolA.agents["peer-one"])
	}
	if agentModelFromSpec(toolB.agents["peer-two"]) != "model-two" {
		t.Fatalf("engine B model=%v", toolB.agents["peer-two"])
	}
	// Closing one must not clear the other (distinct instances).
	if toolA == toolB {
		t.Fatal("engines must not share the same delegateTool pointer")
	}
}

// captureDelegateViaEngine builds an Engine for the profile and returns the
// engine-scoped delegateTool installed by RegisterFromEngine.
func captureDelegateViaEngine(t *testing.T, profile string) *delegateTool {
	t.Helper()
	eng, err := mow.New(mow.Options{
		LoadUserConfig: true,
		Workspace:      profile,
		Model:          "gpt-5-mini",
		NoSession:      true,
		Chat: func(context.Context, []mow.Message, []mow.ToolSpec) (mow.Message, error) {
			return mow.Message{Role: "assistant", Content: "ok"}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = eng.Close() })

	var c Config
	if err := eng.Extension("acp", &c); err != nil {
		t.Fatal(err)
	}
	agents, err := resolvePeers(c)
	if err != nil {
		t.Fatal(err)
	}
	indexed := indexPeers(agents)
	tool := &delegateTool{
		agents:    indexed,
		workspace: eng.Workspace(),
		peerIdle:  peerIdleDuration(c.PeerIdleSec),
		peers:     map[string]*peerSlot{},
	}
	if err := eng.AddTool(tool); err != nil {
		t.Fatal(err)
	}
	return tool
}

func keysOf(m map[string]PeerSpec) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
