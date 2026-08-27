package doctor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunDoesNotNeedMCP(t *testing.T) {
	home := t.TempDir()
	t.Setenv("MOW_HOME", home)
	ws := t.TempDir()
	if err := os.WriteFile(filepath.Join(ws, "mcp.json"), []byte(`{"mcpServers":{"demo":{}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	r := Run(ws)
	if r.Home != home || r.Workspace != ws {
		t.Fatalf("paths: %+v", r)
	}
	text := Format(r)
	if !strings.Contains(text, "mcp") || !strings.Contains(text, "not started") {
		t.Fatalf("want mcp listed as not started:\n%s", text)
	}
	if strings.Contains(strings.ToLower(text), "api_key") {
		t.Fatal("doctor leaked a secret-looking key")
	}
}

func TestRunReportsUnregisteredEnable(t *testing.T) {
	home := t.TempDir()
	t.Setenv("MOW_HOME", home)
	ws := t.TempDir()
	body := "llm:\n  model: gpt-5-mini\ntools:\n  enable: [read, glob, grep, understand_image]\n"
	if err := os.WriteFile(filepath.Join(home, "config.yaml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	text := Format(Run(ws))
	if !strings.Contains(text, "understand_image") {
		t.Fatalf("want understand_image in doctor:\n%s", text)
	}
	if !strings.Contains(text, "not registered") || !strings.Contains(text, "this binary") {
		t.Fatalf("want unregistered-enable wording:\n%s", text)
	}
	if !strings.Contains(text, "packs/media") || !strings.Contains(text, "mowx") {
		t.Fatalf("want lean/mowx hint:\n%s", text)
	}
	if !strings.Contains(text, "FAIL") {
		t.Fatalf("want tools check FAIL:\n%s", text)
	}
}

func TestBundleIsRedacted(t *testing.T) {
	home := t.TempDir()
	t.Setenv("MOW_HOME", home)
	ws := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "config.yaml"), []byte("llm:\n  api_key: sk-secret-do-not-copy\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	path, err := Bundle(ws)
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	body := string(b)
	if strings.Contains(body, "sk-secret") || strings.Contains(body, "api_key") {
		t.Fatalf("bundle copied config secrets:\n%s", body)
	}
	if !strings.Contains(body, "# mow trace") {
		t.Fatalf("not a trace file:\n%s", body)
	}
}
