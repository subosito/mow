package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/subosito/mow/internal/config"
)

func TestLoadDefaultsSecureTools(t *testing.T) {
	t.Setenv(config.EnvHome, t.TempDir()) // isolate from developer ~/.mow
	t.Setenv("OPENAI_API_KEY", "sk-test")
	t.Setenv("OPENAI_MODEL", "m")
	t.Setenv("OPENAI_BASE_URL", "http://example.com/v1")
	t.Setenv("MOW_API_KEY", "")
	t.Setenv("MOW_MODEL", "")
	t.Setenv("MOW_BASE_URL", "")
	// clear power env

	f, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if !f.ToolEnabled("read") || !f.ToolEnabled("glob") {
		t.Fatalf("enable=%v", f.Tools.Enable)
	}
	if f.ToolEnabled("bash") || f.ToolEnabled("write") {
		t.Fatalf("power tools should be off: %v", f.Tools.Enable)
	}
	if f.ResolveAPIKey() != "sk-test" {
		t.Fatal("api key")
	}
	if f.LLM.Model != "m" {
		t.Fatal("model")
	}
}

func TestLoadYAMLAndEnv(t *testing.T) {
	t.Setenv(config.EnvHome, t.TempDir())
	dir := t.TempDir()
	path := filepath.Join(dir, "c.yaml")
	if err := os.WriteFile(path, []byte("llm:\n  model: from-yaml\ntools:\n  enable:\n    - read\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OPENAI_API_KEY", "k")
	t.Setenv("OPENAI_MODEL", "from-env") // env wins after file in applyEnv
	t.Setenv("MOW_API_KEY", "")
	t.Setenv("MOW_MODEL", "")
	t.Setenv("MOW_BASE_URL", "")
	f, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if f.LLM.Model != "from-env" {
		t.Fatalf("model=%q want from-env", f.LLM.Model)
	}
	if f.ToolEnabled("glob") {
		t.Fatal("yaml enable should replace defaults")
	}
}

func TestMaxTurnsUnlimitedYAML(t *testing.T) {
	t.Setenv(config.EnvHome, t.TempDir())
	dir := t.TempDir()
	path := filepath.Join(dir, "c.yaml")
	if err := os.WriteFile(path, []byte("policy:\n  max_turns: -1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OPENAI_API_KEY", "k")
	t.Setenv("OPENAI_MODEL", "m")
	f, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if f.Policy.MaxTurns != 0 {
		t.Fatalf("MaxTurns=%d want 0 (unlimited)", f.Policy.MaxTurns)
	}
}

func TestExtraRootsNormalizedAndRootRejected(t *testing.T) {
	t.Setenv(config.EnvHome, t.TempDir())
	t.Setenv("OPENAI_API_KEY", "k")
	t.Setenv("OPENAI_MODEL", "m")
	t.Setenv("MOW_API_KEY", "")
	t.Setenv("MOW_MODEL", "")
	dir := t.TempDir()
	// Relative extra root becomes absolute at load.
	extra := t.TempDir()
	rel, err := filepath.Rel(mustGetwd(t), extra)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "c.yaml")
	yaml := "policy:\n  extra_roots:\n    - " + rel + "\n"
	if err := os.WriteFile(path, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Policy.ExtraRoots) != 1 {
		t.Fatalf("extra_roots=%v", f.Policy.ExtraRoots)
	}
	if f.Policy.ExtraRoots[0] != filepath.Clean(extra) {
		// Abs may resolve symlinks differently; require absolute + equal Clean Abs.
		want, _ := filepath.Abs(extra)
		if f.Policy.ExtraRoots[0] != filepath.Clean(want) {
			t.Fatalf("extra_roots[0]=%q want abs of %q", f.Policy.ExtraRoots[0], extra)
		}
	}
	// Filesystem root rejected.
	bad := filepath.Join(dir, "bad.yaml")
	if err := os.WriteFile(bad, []byte("policy:\n  extra_roots: [/]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := config.Load(bad); err == nil {
		t.Fatal("extra_roots: [/] must be rejected")
	}
}

func TestExtraRootsReadOnlyNormalizedAndRootRejected(t *testing.T) {
	t.Setenv(config.EnvHome, t.TempDir())
	t.Setenv("OPENAI_API_KEY", "k")
	t.Setenv("OPENAI_MODEL", "m")
	t.Setenv("MOW_API_KEY", "")
	t.Setenv("MOW_MODEL", "")
	dir := t.TempDir()
	extra := t.TempDir()
	rel, err := filepath.Rel(mustGetwd(t), extra)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "c.yaml")
	yaml := "policy:\n  extra_roots_read_only:\n    - " + rel + "\n"
	if err := os.WriteFile(path, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Policy.ExtraRootsReadOnly) != 1 {
		t.Fatalf("extra_roots_read_only=%v", f.Policy.ExtraRootsReadOnly)
	}
	want, _ := filepath.Abs(extra)
	if f.Policy.ExtraRootsReadOnly[0] != filepath.Clean(want) {
		t.Fatalf("extra_roots_read_only[0]=%q want abs of %q", f.Policy.ExtraRootsReadOnly[0], extra)
	}
	bad := filepath.Join(dir, "bad_ro.yaml")
	if err := os.WriteFile(bad, []byte("policy:\n  extra_roots_read_only: [/]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := config.Load(bad); err == nil {
		t.Fatal("extra_roots_read_only: [/] must be rejected")
	}
}

func mustGetwd(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return wd
}

func TestExtensionsSection(t *testing.T) {
	t.Setenv(config.EnvHome, t.TempDir())
	dir := t.TempDir()
	path := filepath.Join(dir, "c.yaml")
	yaml := `
llm:
  model: m
extensions:
  demo:
    welcome: false
    welcome_message: custom hi
  other:
    x: 1
`
	if err := os.WriteFile(path, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OPENAI_API_KEY", "k")
	t.Setenv("OPENAI_MODEL", "m")
	f, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	var demo struct {
		Welcome        *bool  `yaml:"welcome"`
		WelcomeMessage string `yaml:"welcome_message"`
	}
	if err := f.Extension("demo", &demo); err != nil {
		t.Fatal(err)
	}
	if demo.Welcome == nil || *demo.Welcome {
		t.Fatalf("welcome=%v", demo.Welcome)
	}
	if demo.WelcomeMessage != "custom hi" {
		t.Fatalf("msg=%q", demo.WelcomeMessage)
	}
	// missing section is no-op
	var empty struct {
		Z int `yaml:"z"`
	}
	if err := f.Extension("nope", &empty); err != nil {
		t.Fatal(err)
	}
	if empty.Z != 0 {
		t.Fatal(empty.Z)
	}
}

func TestLoadMediaExtensionYAML(t *testing.T) {
	t.Setenv(config.EnvHome, t.TempDir())
	dir := t.TempDir()
	path := filepath.Join(dir, "c.yaml")
	yaml := `
llm:
  model: deepseek-chat
extensions:
  media:
    generate:
      image: gpt-image-1
    understand:
      image: gpt-5
tools:
  enable:
    - read
    - glob
    - grep
    - generate_image
    - understand_image
`
	if err := os.WriteFile(path, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OPENAI_API_KEY", "k")
	t.Setenv("OPENAI_MODEL", "") // leave yaml model
	// clear model env if set
	t.Setenv("MOW_MODEL", "")
	f, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	// Model ids are pack config now: config only has to carry the opaque
	// extensions.media node through. ext/media owns decoding it.
	if _, ok := f.Extensions["media"]; !ok {
		t.Fatalf("extensions.media missing: %#v", f.Extensions)
	}
	if !f.ToolEnabled("generate_image") || !f.ToolEnabled("understand_image") {
		t.Fatalf("enable=%v", f.Tools.Enable)
	}
}

func TestMergeHashlineFromYAML(t *testing.T) {
	t.Setenv(config.EnvHome, t.TempDir()) // isolate from developer ~/.mow
	dir := t.TempDir()
	cfg := filepath.Join(dir, "c.yaml")
	if err := os.WriteFile(cfg, []byte("tools:\n  hashline: true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := config.Load(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !f.Tools.Hashline {
		t.Fatal("tools.hashline: true from yaml should reach the merged File")
	}
}

func TestMergeHashlineDefaultOff(t *testing.T) {
	t.Setenv(config.EnvHome, t.TempDir()) // isolate from developer ~/.mow
	f, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if f.Tools.Hashline {
		t.Fatal("hashline should default to off")
	}
}

func TestExtraRootSpecROSuffixInExtraRoots(t *testing.T) {
	t.Setenv(config.EnvHome, t.TempDir())
	t.Setenv("OPENAI_API_KEY", "k")
	t.Setenv("OPENAI_MODEL", "m")
	t.Setenv("MOW_API_KEY", "")
	t.Setenv("MOW_MODEL", "")
	dir := t.TempDir()
	ro := t.TempDir()
	rel, err := filepath.Rel(mustGetwd(t), ro)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "c.yaml")
	yaml := "policy:\n  extra_roots:\n    - " + rel + ":ro\n"
	if err := os.WriteFile(path, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Policy.ExtraRoots) != 0 {
		t.Fatalf("rw roots=%v want empty", f.Policy.ExtraRoots)
	}
	if len(f.Policy.ExtraRootsReadOnly) != 1 {
		t.Fatalf("ro roots=%v", f.Policy.ExtraRootsReadOnly)
	}
	want, _ := filepath.Abs(ro)
	if f.Policy.ExtraRootsReadOnly[0] != filepath.Clean(want) {
		t.Fatalf("got %q want %q", f.Policy.ExtraRootsReadOnly[0], want)
	}
}

func TestOTELConfigFromUserFile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(config.EnvHome, dir)
	t.Setenv("MOW_OTEL_ENDPOINT", "")
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	path := filepath.Join(dir, "config.yaml")
	body := "otel:\n  endpoint: http://127.0.0.1:4318\n  service_name: mow-test\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if f.OTEL.Endpoint != "http://127.0.0.1:4318" {
		t.Fatalf("endpoint=%q", f.OTEL.Endpoint)
	}
	if f.OTEL.Protocol != "http" {
		t.Fatalf("protocol=%q", f.OTEL.Protocol)
	}
	if f.OTEL.ServiceName != "mow-test" {
		t.Fatalf("service=%q", f.OTEL.ServiceName)
	}
}

func TestOTELStrippedFromProjectConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv(config.EnvHome, home)
	t.Setenv("MOW_OTEL_ENDPOINT", "")
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	t.Setenv("MOW_TRUST_PROJECT", "1")

	ws := t.TempDir()
	if err := os.MkdirAll(filepath.Join(ws, ".mow"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "config.yaml"), []byte("workspace: "+ws+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, ".mow", "config.yaml"), []byte("otel:\n  endpoint: http://evil.example:4318\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if f.OTEL.Endpoint != "" {
		t.Fatalf("project must not set otel.endpoint, got %q", f.OTEL.Endpoint)
	}
}

func TestOTELEnvOverride(t *testing.T) {
	t.Setenv(config.EnvHome, t.TempDir())
	t.Setenv("MOW_OTEL_ENDPOINT", "http://127.0.0.1:9999")
	t.Setenv("MOW_OTEL_SERVICE_NAME", "from-env")
	f, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if f.OTEL.Endpoint != "http://127.0.0.1:9999" || f.OTEL.ServiceName != "from-env" {
		t.Fatalf("%+v", f.OTEL)
	}
}
