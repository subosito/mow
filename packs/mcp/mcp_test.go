package mcp

import (
	"context"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

func TestConfigResolvedMapAndList(t *testing.T) {
	// Standard mcpServers map: key becomes the name, entries are name-ordered.
	c := Config{
		MCPServers: map[string]ServerConfig{
			"fs":  {Command: "npx"},
			"api": {URL: "https://mcp.example/x"},
		},
		Servers: []ServerConfig{{Name: "legacy", Command: "foo"}},
	}
	got := c.resolved()
	if len(got) != 3 {
		t.Fatalf("resolved=%d want 3", len(got))
	}
	// map entries sorted by key first, then the list appended
	if got[0].Name != "api" || got[1].Name != "fs" || got[2].Name != "legacy" {
		t.Fatalf("order/names: %+v", []string{got[0].Name, got[1].Name, got[2].Name})
	}
	if got[1].Command != "npx" {
		t.Fatalf("fs command lost: %+v", got[1])
	}
}

func TestConfigMapDecodesFromJSON(t *testing.T) {
	// The ecosystem-standard .mcp.json shape decodes via the yaml decoder.
	const std = `{"mcpServers":{"fs":{"command":"npx","args":["-y","srv"]}}}`
	var c Config
	if err := yaml.Unmarshal([]byte(std), &c); err != nil {
		t.Fatal(err)
	}
	got := c.resolved()
	if len(got) != 1 || got[0].Name != "fs" || got[0].Command != "npx" || len(got[0].Args) != 2 {
		t.Fatalf("standard json not parsed: %+v", got)
	}
}

func TestMCPToolUntrusted(t *testing.T) {
	var tool mcpTool
	if !tool.Untrusted() {
		t.Fatal("mcp tools must frame output as untrusted")
	}
}

func TestCallTimeoutDefaultAndOverride(t *testing.T) {
	if (ServerConfig{}).callTimeout() != defaultMCPCallTimeout {
		t.Fatalf("zero timeout_sec = %v want %v", ServerConfig{}.callTimeout(), defaultMCPCallTimeout)
	}
	if (ServerConfig{TimeoutSec: 5}).callTimeout() != 5*time.Second {
		t.Fatalf("timeout_sec 5 = %v", ServerConfig{TimeoutSec: 5}.callTimeout())
	}
	if (ServerConfig{TimeoutSec: -1}).callTimeout() != 0 {
		t.Fatalf("negative timeout_sec should disable extra deadline")
	}
}

func TestWithCallTimeoutRespectsShorterParentDeadline(t *testing.T) {
	parent, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	ctx, stop := withCallTimeout(parent, time.Second)
	defer stop()
	deadline, ok := ctx.Deadline()
	if !ok {
		t.Fatal("expected deadline")
	}
	if time.Until(deadline) > 200*time.Millisecond {
		t.Fatalf("parent deadline should win: until %v", time.Until(deadline))
	}
}
