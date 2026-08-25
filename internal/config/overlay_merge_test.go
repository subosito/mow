package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/subosito/mow/internal/config"
)

func isolateOverlayHome(t *testing.T) {
	t.Helper()
	t.Setenv(config.EnvHome, t.TempDir())
	for _, k := range []string{
		"OPENAI_API_KEY", "OPENAI_MODEL", "OPENAI_BASE_URL",
		"MOW_API_KEY", "MOW_MODEL", "MOW_BASE_URL", "MOW_WIRE",
		"ANTHROPIC_API_KEY", "ANTHROPIC_MODEL", "ANTHROPIC_BASE_URL",
	} {
		t.Setenv(k, "")
	}
	t.Setenv("OPENAI_API_KEY", "k")
	t.Setenv("OPENAI_MODEL", "m")
}

func writeYAML(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func wantCache(t *testing.T, got *config.CacheMode, want config.CacheMode) {
	t.Helper()
	if got == nil {
		t.Fatalf("prompt_cache=nil want %q", want)
	}
	if *got != want {
		t.Fatalf("prompt_cache=%q want %q", *got, want)
	}
}

func TestLoadPathsMergesDroppedHostFields(t *testing.T) {
	isolateOverlayHome(t)
	path := writeYAML(t, t.TempDir(), "c.yaml", `
llm:
  max_tokens: 32000
  prompt_cache: none
  context_window: 200000
  input_price: 1.5
  output_price: 6
policy:
  max_run_tokens: 2000000
  max_run_usd: 5
  compact_summary: true
`)
	f, err := config.LoadPaths(path)
	if err != nil {
		t.Fatal(err)
	}
	if f.LLM.MaxTokens != 32000 {
		t.Fatalf("MaxTokens=%d want 32000", f.LLM.MaxTokens)
	}
	wantCache(t, f.LLM.PromptCache, config.CacheModeNone)
	if f.LLM.ContextWindow != 200000 {
		t.Fatalf("ContextWindow=%d want 200000", f.LLM.ContextWindow)
	}
	if f.LLM.InputPrice != 1.5 || f.LLM.OutputPrice != 6 {
		t.Fatalf("prices=%v/%v want 1.5/6", f.LLM.InputPrice, f.LLM.OutputPrice)
	}
	if f.Policy.MaxRunTokens != 2_000_000 {
		t.Fatalf("MaxRunTokens=%d want 2000000", f.Policy.MaxRunTokens)
	}
	if f.Policy.MaxRunUSD != 5 {
		t.Fatalf("MaxRunUSD=%v want 5", f.Policy.MaxRunUSD)
	}
	if !f.Policy.CompactSummary {
		t.Fatal("CompactSummary=false want true")
	}
}

func TestLoadPathsLaterHostOverlayWinsDroppedFields(t *testing.T) {
	isolateOverlayHome(t)
	dir := t.TempDir()
	first := writeYAML(t, dir, "a.yaml", `
llm:
  max_tokens: 1000
  prompt_cache: short
  context_window: 100000
  input_price: 1
  output_price: 2
policy:
  max_run_tokens: 10000
  max_run_usd: 1
  compact_summary: true
`)
	second := writeYAML(t, dir, "b.yaml", `
llm:
  max_tokens: 32000
  prompt_cache: none
  context_window: 200000
  input_price: 1.5
  output_price: 6
policy:
  max_run_tokens: 2000000
  max_run_usd: 5
`)
	f, err := config.LoadPaths(first, second)
	if err != nil {
		t.Fatal(err)
	}
	if f.LLM.MaxTokens != 32000 {
		t.Fatalf("MaxTokens=%d want 32000 (later file)", f.LLM.MaxTokens)
	}
	wantCache(t, f.LLM.PromptCache, config.CacheModeNone)
	if f.LLM.ContextWindow != 200000 {
		t.Fatalf("ContextWindow=%d want 200000", f.LLM.ContextWindow)
	}
	if f.LLM.InputPrice != 1.5 || f.LLM.OutputPrice != 6 {
		t.Fatalf("prices=%v/%v want 1.5/6", f.LLM.InputPrice, f.LLM.OutputPrice)
	}
	if f.Policy.MaxRunTokens != 2_000_000 {
		t.Fatalf("MaxRunTokens=%d want 2000000", f.Policy.MaxRunTokens)
	}
	if f.Policy.MaxRunUSD != 5 {
		t.Fatalf("MaxRunUSD=%v want 5", f.Policy.MaxRunUSD)
	}
	if !f.Policy.CompactSummary {
		t.Fatal("CompactSummary dropped by later overlay that omitted it")
	}
}

func TestLoadHostFileMergesDroppedFields(t *testing.T) {
	isolateOverlayHome(t)
	path := writeYAML(t, t.TempDir(), "c.yaml", `
llm:
  max_tokens: 4096
  prompt_cache: long
  context_window: 128000
  input_price: 3
  output_price: 15
policy:
  max_run_tokens: 50000
  max_run_usd: 2.5
  compact_summary: true
`)
	f, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if f.LLM.MaxTokens != 4096 {
		t.Fatalf("MaxTokens=%d want 4096", f.LLM.MaxTokens)
	}
	wantCache(t, f.LLM.PromptCache, config.CacheModeLong)
	if f.LLM.ContextWindow != 128000 || f.LLM.InputPrice != 3 || f.LLM.OutputPrice != 15 {
		t.Fatalf("metering=%d/%v/%v", f.LLM.ContextWindow, f.LLM.InputPrice, f.LLM.OutputPrice)
	}
	if f.Policy.MaxRunTokens != 50_000 || f.Policy.MaxRunUSD != 2.5 || !f.Policy.CompactSummary {
		t.Fatalf("budget=%d/%v/%v", f.Policy.MaxRunTokens, f.Policy.MaxRunUSD, f.Policy.CompactSummary)
	}
}

func TestProjectOverlayCannotSetBudgetFields(t *testing.T) {
	isolateOverlayHome(t)
	t.Setenv("MOW_TRUST_PROJECT", "1")
	ws := t.TempDir()
	if err := os.MkdirAll(filepath.Join(ws, ".mow"), 0o755); err != nil {
		t.Fatal(err)
	}
	host := writeYAML(t, t.TempDir(), "user.yaml", `
workspace: `+ws+`
llm:
  max_tokens: 4096
  prompt_cache: long
  context_window: 128000
  input_price: 3
  output_price: 15
policy:
  max_run_tokens: 50000
  max_run_usd: 2.5
`)
	project := `
llm:
  max_tokens: 999999
  prompt_cache: none
  context_window: 1
  input_price: 0.01
  output_price: 0.01
policy:
  max_run_tokens: 1
  max_run_usd: 999
  compact_summary: true
  max_turns: 7
`
	if err := os.WriteFile(filepath.Join(ws, ".mow", "config.yaml"), []byte(project), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := config.Load(host)
	if err != nil {
		t.Fatal(err)
	}
	if f.LLM.MaxTokens != 4096 {
		t.Fatalf("project raised/changed max_tokens: %d", f.LLM.MaxTokens)
	}
	wantCache(t, f.LLM.PromptCache, config.CacheModeLong)
	if f.LLM.ContextWindow != 128000 || f.LLM.InputPrice != 3 || f.LLM.OutputPrice != 15 {
		t.Fatalf("project changed metering: %d/%v/%v", f.LLM.ContextWindow, f.LLM.InputPrice, f.LLM.OutputPrice)
	}
	if f.Policy.MaxRunTokens != 50_000 {
		t.Fatalf("project changed max_run_tokens: %d", f.Policy.MaxRunTokens)
	}
	if f.Policy.MaxRunUSD != 2.5 {
		t.Fatalf("project changed max_run_usd: %v", f.Policy.MaxRunUSD)
	}
	if f.Policy.CompactSummary {
		t.Fatal("project must not enable compact_summary")
	}
	if f.Policy.MaxTurns != 7 {
		t.Fatalf("benign project max_turns=%d want 7", f.Policy.MaxTurns)
	}
}

func TestProjectOverlayCannotIntroduceBudgetFields(t *testing.T) {
	isolateOverlayHome(t)
	t.Setenv("MOW_TRUST_PROJECT", "1")
	ws := t.TempDir()
	if err := os.MkdirAll(filepath.Join(ws, ".mow"), 0o755); err != nil {
		t.Fatal(err)
	}
	host := writeYAML(t, t.TempDir(), "user.yaml", "workspace: "+ws+"\n")
	if err := os.WriteFile(filepath.Join(ws, ".mow", "config.yaml"), []byte(`
llm:
  max_tokens: 32000
  prompt_cache: none
  context_window: 200000
  input_price: 9
  output_price: 9
policy:
  max_run_tokens: 2000000
  max_run_usd: 50
  compact_summary: true
`), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := config.Load(host)
	if err != nil {
		t.Fatal(err)
	}
	if f.LLM.MaxTokens != 0 || f.LLM.PromptCache != nil || f.LLM.ContextWindow != 0 {
		t.Fatalf("project introduced llm spend knobs: tokens=%d cache=%v cw=%d",
			f.LLM.MaxTokens, f.LLM.PromptCache, f.LLM.ContextWindow)
	}
	if f.LLM.InputPrice != 0 || f.LLM.OutputPrice != 0 {
		t.Fatalf("project introduced prices: %v/%v", f.LLM.InputPrice, f.LLM.OutputPrice)
	}
	if f.Policy.MaxRunTokens != 0 || f.Policy.MaxRunUSD != 0 || f.Policy.CompactSummary {
		t.Fatalf("project introduced budget: tokens=%d usd=%v summary=%v",
			f.Policy.MaxRunTokens, f.Policy.MaxRunUSD, f.Policy.CompactSummary)
	}
}

// tools.write/shell are the config form of --allow-write/--allow-shell:
// granting power tools is a host/user trust decision, so a project overlay
// that sets them must be stripped.
func TestProjectOverlayCannotSetToolWriteShell(t *testing.T) {
	isolateOverlayHome(t)
	t.Setenv("MOW_TRUST_PROJECT", "1")
	ws := t.TempDir()
	if err := os.MkdirAll(filepath.Join(ws, ".mow"), 0o755); err != nil {
		t.Fatal(err)
	}
	host := writeYAML(t, t.TempDir(), "user.yaml", "workspace: "+ws+"\n")
	if err := os.WriteFile(filepath.Join(ws, ".mow", "config.yaml"), []byte(`
tools:
  write: true
  shell: true
`), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := config.Load(host)
	if err != nil {
		t.Fatal(err)
	}
	if f.Tools.Write || f.Tools.Shell {
		t.Fatalf("project granted power tools: write=%v shell=%v", f.Tools.Write, f.Tools.Shell)
	}
	for _, name := range []string{"write", "edit", "bash"} {
		if f.ToolEnabled(name) {
			t.Fatalf("project enabled power tool %q", name)
		}
	}
}
