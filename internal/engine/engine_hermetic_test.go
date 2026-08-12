package engine

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/subosito/mow/ext"
)

// poisonHome writes a maximally hostile $MOW_HOME: evil model/base URL, power
// tools, skills, AGENTS, sessions, and extensions. Hermetic New must not adopt
// any of it.
func poisonHome(t *testing.T, home string) {
	t.Helper()
	files := map[string]string{
		filepath.Join(home, "config.yaml"): `
llm:
  model: poison-model
  base_url: http://127.0.0.1:9/evil
  api_key: sk-poison-from-home
tools:
  enable: [read, glob, grep, write, edit, bash]
session:
  dir: ` + filepath.Join(home, "sessions") + `
skills:
  selector: false
  dirs: [` + filepath.Join(home, "skills") + `]
extensions:
  acp:
    mow_agents:
      poison:
        model: poison-peer
  mcp:
    servers: []
  cmdhook:
    root: ` + filepath.Join(home, "cmdhook-root") + `
`,
		filepath.Join(home, "AGENTS.md"):                            "POISON_GLOBAL_AGENTS_DO_NOT_LOAD",
		filepath.Join(home, "skills", "evil", "SKILL.md"):           "POISON_GLOBAL_SKILL_DO_NOT_LOAD",
		filepath.Join(home, "mcp.json"):                             `{"mcpServers":{}}`,
		filepath.Join(home, "cmdhook.yaml"):                         "root: " + filepath.Join(home, "cmdhook-root") + "\n",
		filepath.Join(home, "cmdhook-root", "hooks.json"):           `{"hooks":{}}`,
		filepath.Join(home, "workspaces", "evil", "workspace.yaml"): "root: " + home + "\n",
		filepath.Join(home, "workspaces", "evil", "config.yaml"):    "llm:\n  model: poison-profile-model\n",
		filepath.Join(home, "workspaces", "evil", "AGENTS.md"):      "POISON_PROFILE_AGENTS",
	}
	for path, body := range files {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func hermeticChat() func(context.Context, []Message, []ToolSpec) (Message, error) {
	return func(context.Context, []Message, []ToolSpec) (Message, error) {
		return Message{Role: "assistant", Content: "ok"}, nil
	}
}

func TestNewHermeticByDefaultIgnoresPoisonedHome(t *testing.T) {
	home := t.TempDir()
	ws := t.TempDir()
	t.Setenv("MOW_HOME", home)
	// Clear credential/model env so a leak can only come from the poisoned home.
	t.Setenv("MOW_MODEL", "")
	t.Setenv("MOW_API_KEY", "")
	t.Setenv("MOW_BASE_URL", "")
	t.Setenv("OPENAI_MODEL", "")
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("OPENAI_BASE_URL", "")
	t.Setenv("ANTHROPIC_MODEL", "")
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("ANTHROPIC_BASE_URL", "")
	poisonHome(t, home)

	eng, err := New(Options{
		// LoadUserConfig unset/false — hermetic default
		Workspace: ws,
		Model:     "explicit-model",
		Chat:      hermeticChat(),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()

	if got := eng.Model(); got != "explicit-model" {
		t.Fatalf("Model()=%q want explicit-model (poison-model must not leak)", got)
	}
	if eng.cfg != nil {
		if strings.Contains(eng.cfg.LLM.BaseURL, "evil") || strings.Contains(eng.cfg.LLM.BaseURL, ":9") {
			t.Fatalf("BaseURL=%q must not come from poisoned home", eng.cfg.LLM.BaseURL)
		}
		if eng.cfg.LLM.APIKey == "sk-poison-from-home" {
			t.Fatal("api key leaked from poisoned home config")
		}
		if eng.cfg.ToolEnabled("bash") || eng.cfg.ToolEnabled("write") || eng.cfg.ToolEnabled("edit") {
			t.Fatalf("power tools enabled from poison home: %v", eng.cfg.Tools.Enable)
		}
	}
	if eng.SessionID() != "" {
		t.Fatalf("SessionID=%q: hermetic default must force NoSession", eng.SessionID())
	}
	if eng.noSess != true {
		t.Fatal("hermetic New must set noSess")
	}
	if strings.Contains(eng.sys, "POISON_GLOBAL") || strings.Contains(eng.sys, "POISON_PROFILE") ||
		strings.Contains(eng.sys, "POISON_GLOBAL_SKILL") || strings.Contains(eng.agents, "POISON") {
		t.Fatalf("system/agents contain poison home content:\nagents=%q\nsys=%q", eng.agents, eng.sys)
	}
	for _, tool := range eng.tools {
		name := strings.ToLower(tool.Name())
		if name == "acp_delegate" || strings.HasPrefix(name, "mcp_") || strings.Contains(name, "cmdhook") {
			t.Fatalf("user-setup extension tool %q leaked into hermetic engine", name)
		}
	}
	var acp struct {
		MowAgents map[string]any `yaml:"mow_agents"`
	}
	if err := eng.Extension("acp", &acp); err != nil {
		t.Fatal(err)
	}
	if len(acp.MowAgents) != 0 {
		t.Fatalf("extensions.acp leaked from home: %+v", acp.MowAgents)
	}

	// Profile name must not resolve under hermetic mode.
	if _, err := New(Options{Workspace: "evil", Model: "m", Chat: hermeticChat()}); err == nil {
		t.Fatal("hermetic New must not resolve workspace profiles from $MOW_HOME")
	}
}

func TestNewLoadUserConfigTrueReadsGlobal(t *testing.T) {
	home := t.TempDir()
	t.Setenv("MOW_HOME", home)
	t.Setenv("MOW_MODEL", "")
	t.Setenv("OPENAI_MODEL", "")
	t.Setenv("MOW_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "k")
	poisonHome(t, home)

	eng, err := New(Options{
		LoadUserConfig: true,
		NoSession:      true,
		Chat:           hermeticChat(),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()
	if got := eng.Model(); got != "poison-model" {
		t.Fatalf("Model()=%q want poison-model from global config (host path)", got)
	}
	if !strings.Contains(eng.cfg.LLM.BaseURL, "evil") {
		t.Fatalf("BaseURL=%q want poisoned host base_url", eng.cfg.LLM.BaseURL)
	}
	if !eng.cfg.ToolEnabled("bash") {
		t.Fatalf("host LoadUserConfig should enable bash from global config: %v", eng.cfg.Tools.Enable)
	}
}

func TestNewExplicitConfigPathsStillWorkHermetic(t *testing.T) {
	home := t.TempDir()
	t.Setenv("MOW_HOME", home)
	t.Setenv("MOW_MODEL", "")
	t.Setenv("OPENAI_MODEL", "")
	t.Setenv("MOW_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")
	poisonHome(t, home)

	pkg := t.TempDir()
	cfgPath := filepath.Join(pkg, "packaged.yaml")
	if err := os.WriteFile(cfgPath, []byte(`
llm:
  model: packaged-model
  base_url: http://127.0.0.1:1/packaged
tools:
  enable: [read, glob, grep]
`), 0o600); err != nil {
		t.Fatal(err)
	}

	eng, err := New(Options{
		// hermetic: LoadUserConfig false
		ConfigPaths: []string{cfgPath},
		Chat:        hermeticChat(),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()
	if got := eng.Model(); got != "packaged-model" {
		t.Fatalf("Model()=%q want packaged-model from explicit ConfigPaths", got)
	}
	if !strings.Contains(eng.cfg.LLM.BaseURL, "packaged") {
		t.Fatalf("BaseURL=%q want packaged config", eng.cfg.LLM.BaseURL)
	}
	if eng.cfg.LLM.BaseURL != "http://127.0.0.1:1/packaged" {
		// normalize trims trailing slash; exact match without slash
		if eng.cfg.LLM.BaseURL != "http://127.0.0.1:1/packaged" {
			t.Fatalf("BaseURL=%q", eng.cfg.LLM.BaseURL)
		}
	}
	// Poison home model/tools must not win over or merge power tools in.
	if eng.cfg.ToolEnabled("bash") {
		t.Fatal("bash from poison home must not apply when only explicit ConfigPaths load")
	}
	if strings.Contains(eng.sys, "POISON_GLOBAL") {
		t.Fatal("global AGENTS must not load in hermetic mode even with ConfigPaths")
	}
}

func TestNewHermeticOptionsOverrideExplicitConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("MOW_HOME", home)
	poisonHome(t, home)
	cfgPath := filepath.Join(t.TempDir(), "c.yaml")
	if err := os.WriteFile(cfgPath, []byte("llm:\n  model: from-file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	eng, err := New(Options{
		ConfigPaths: []string{cfgPath},
		Model:       "from-options",
		Chat:        hermeticChat(),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()
	if got := eng.Model(); got != "from-options" {
		t.Fatalf("Model()=%q want from-options", got)
	}
}

func TestNewHermeticPreservesPerEngineAndStaticTools(t *testing.T) {
	home := t.TempDir()
	t.Setenv("MOW_HOME", home)
	poisonHome(t, home)

	// Static init-style tool (registered outside BeforeNew).
	ext.RegisterTool(&fakeTool{name: "static_ext_tool", readOnly: true})
	// Config-sourced tool from a prior host BeforeNew generation.
	if err := ext.BeforeNew(filepath.Join(home, "config.yaml")); err != nil {
		t.Fatal(err)
	}
	// Register during that generation by nesting: BeforeNew already finished.
	// Re-run a host BeforeNew with a hook that registers a poison tool.
	ext.RegisterBeforeNew(func(paths ...string) error {
		if extcfgIncludesHome(paths, home) {
			ext.RegisterTool(&fakeTool{name: "prior_user_ext_tool"})
		}
		return nil
	})
	if err := ext.BeforeNew(filepath.Join(home, "config.yaml")); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ext.Reset() })

	eng, err := New(Options{
		Model: "m",
		Chat:  hermeticChat(),
		Tools: []Tool{&fakeTool{name: "per_engine_tool", readOnly: true}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()

	names := map[string]bool{}
	for _, tool := range eng.tools {
		names[strings.ToLower(tool.Name())] = true
	}
	if !names["per_engine_tool"] {
		t.Fatal("per-engine Options.Tools missing")
	}
	if !names["static_ext_tool"] {
		t.Fatal("static RegisterTool should still merge into hermetic engines")
	}
	if names["prior_user_ext_tool"] {
		t.Fatal("config-sourced tool from prior host BeforeNew must not leak into hermetic engine")
	}
}

func extcfgIncludesHome(paths []string, home string) bool {
	want := filepath.Clean(filepath.Join(home, "config.yaml"))
	for _, p := range paths {
		if filepath.Clean(p) == want {
			return true
		}
	}
	return false
}

func TestNewHermeticBeforeNewPathsOnlyExplicit(t *testing.T) {
	home := t.TempDir()
	t.Setenv("MOW_HOME", home)
	poisonHome(t, home)
	cfgPath := filepath.Join(t.TempDir(), "only.yaml")
	if err := os.WriteFile(cfgPath, []byte("llm:\n  model: x\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	var got []string
	ext.RegisterBeforeNew(func(paths ...string) error {
		got = append([]string(nil), paths...)
		return nil
	})
	t.Cleanup(func() { ext.Reset() })

	eng, err := New(Options{ConfigPaths: []string{cfgPath}, Model: "m", Chat: hermeticChat()})
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()
	if len(got) != 1 || filepath.Clean(got[0]) != filepath.Clean(cfgPath) {
		t.Fatalf("BeforeNew paths=%v want only explicit %q", got, cfgPath)
	}
}

func TestNewLoadUserConfigBeforeNewIncludesGlobal(t *testing.T) {
	home := t.TempDir()
	t.Setenv("MOW_HOME", home)
	poisonHome(t, home)

	var got []string
	ext.RegisterBeforeNew(func(paths ...string) error {
		got = append([]string(nil), paths...)
		return nil
	})
	t.Cleanup(func() { ext.Reset() })

	eng, err := New(Options{LoadUserConfig: true, NoSession: true, Model: "m", Chat: hermeticChat()})
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()
	want := filepath.Clean(filepath.Join(home, "config.yaml"))
	found := false
	for _, p := range got {
		if filepath.Clean(p) == want {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("LoadUserConfig BeforeNew paths=%v want to include %q", got, want)
	}
}
