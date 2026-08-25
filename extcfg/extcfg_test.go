package extcfg_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/subosito/mow/extcfg"
)

type mcpSection struct {
	Servers []struct {
		Name    string `yaml:"name"`
		Command string `yaml:"command"`
	} `yaml:"servers"`
	Keep string `yaml:"keep"`
}

func writeCfg(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestDecodeSection(t *testing.T) {
	dir := t.TempDir()
	path := writeCfg(t, dir, "c.yaml", `
extensions:
  mcp:
    servers:
      - name: demo
        command: true
`)
	var c mcpSection
	ok, err := extcfg.DecodeSection("mcp", []string{path}, &c)
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	if len(c.Servers) != 1 || c.Servers[0].Name != "demo" {
		t.Fatalf("%+v", c)
	}
}

func TestDecodeSectionLaterFileWins(t *testing.T) {
	dir := t.TempDir()
	global := writeCfg(t, dir, "global.yaml", `
extensions:
  mcp:
    servers:
      - name: global
    keep: from-global
`)
	profile := writeCfg(t, dir, "profile.yaml", `
extensions:
  mcp:
    servers:
      - name: profile
`)
	explicit := writeCfg(t, dir, "explicit.yaml", `
extensions:
  mcp:
    servers:
      - name: explicit
`)
	var c mcpSection
	ok, err := extcfg.DecodeSection("mcp", []string{global, profile, explicit}, &c)
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	if len(c.Servers) != 1 || c.Servers[0].Name != "explicit" {
		t.Fatalf("want explicit last-wins, got %+v", c)
	}
	if c.Keep != "" {
		t.Fatalf("later section must replace wholesale, keep=%q leaked from global", c.Keep)
	}
}

func TestDecodeSectionProfileWinsOverGlobalWhenExplicitOmits(t *testing.T) {
	dir := t.TempDir()
	global := writeCfg(t, dir, "global.yaml", `
extensions:
  mcp:
    servers:
      - name: global
    keep: from-global
`)
	profile := writeCfg(t, dir, "profile.yaml", `
extensions:
  mcp:
    servers:
      - name: profile
`)
	explicit := writeCfg(t, dir, "explicit.yaml", "llm:\n  model: m\n")
	missing := filepath.Join(dir, "missing.yaml")
	var c mcpSection
	ok, err := extcfg.DecodeSection("mcp", []string{global, profile, missing, explicit}, &c)
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	if len(c.Servers) != 1 || c.Servers[0].Name != "profile" {
		t.Fatalf("want profile, got %+v", c)
	}
	if c.Keep != "" {
		t.Fatalf("profile must replace global wholesale, keep=%q", c.Keep)
	}
}

func TestDecodeSectionMissingSection(t *testing.T) {
	dir := t.TempDir()
	path := writeCfg(t, dir, "c.yaml", "llm:\n  model: m\n")
	var c mcpSection
	c.Keep = "preset"
	ok, err := extcfg.DecodeSection("mcp", []string{path, filepath.Join(dir, "nope.yaml")}, &c)
	if err != nil {
		t.Fatalf("absent section must not error: %v", err)
	}
	if ok {
		t.Fatal("ok=true for missing extensions.mcp")
	}
	if c.Keep != "preset" {
		t.Fatalf("dst mutated without a hit: %+v", c)
	}
}

func TestDecodeSectionMalformedYAML(t *testing.T) {
	dir := t.TempDir()
	good := writeCfg(t, dir, "good.yaml", `
extensions:
  mcp:
    servers:
      - name: good
`)
	bad := writeCfg(t, dir, "bad.yaml", "extensions: [\n")
	var c mcpSection
	ok, err := extcfg.DecodeSection("mcp", []string{good, bad}, &c)
	if err == nil {
		t.Fatal("malformed later file must fail")
	}
	if ok {
		t.Fatal("ok=true on yaml error")
	}
	if !strings.Contains(err.Error(), "yaml") && !strings.Contains(strings.ToLower(err.Error()), "did not find") {
		// yaml.v3 errors vary; require a parse failure, not a silent skip.
		t.Logf("yaml error: %v", err)
	}
	if len(c.Servers) != 0 {
		t.Fatalf("dst must stay unmodified on error, got %+v", c)
	}

	ok, err = extcfg.DecodeSection("mcp", []string{bad, good}, &c)
	if err == nil {
		t.Fatal("malformed earlier file must fail even if a later file is valid")
	}
	if ok {
		t.Fatal("ok=true on yaml error")
	}
}
