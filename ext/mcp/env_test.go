package mcp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestStartServerScrubsCredentialEnv: an MCP stdio server is a child process
// this agent spawns, so it must not inherit mow's own credentials. Values the
// server's env: map names explicitly are still handed over (that is how an
// operator gives a server its token deliberately).
func TestStartServerScrubsCredentialEnv(t *testing.T) {
	setupMCPTest(t)
	t.Setenv("PROBE_API_KEY", "inherited-key")
	t.Setenv("PROBE_GITHUB_TOKEN", "inherited-token")
	t.Setenv("PROBE_PLAIN", "visible")

	dump := filepath.Join(t.TempDir(), "env.txt")
	cfg := helperConfig(t, "envy", "env")
	cfg.Env["MCP_ENV_DUMP"] = dump
	cfg.Env["PROBE_API_KEY"] = "explicit-key"

	c, err := startServer(cfg)
	if err != nil {
		t.Fatalf("startServer: %v", err)
	}
	defer c.Close()

	raw, err := os.ReadFile(dump)
	if err != nil {
		t.Fatalf("helper env dump: %v", err)
	}
	env := string(raw)
	if !strings.Contains(env, "PROBE_PLAIN=visible") {
		t.Fatalf("ordinary env must reach the server: %q", env)
	}
	if strings.Contains(env, "inherited-key") || strings.Contains(env, "inherited-token") {
		t.Fatalf("parent credential leaked into the server: %q", env)
	}
	if strings.Contains(env, "PROBE_GITHUB_TOKEN") {
		t.Fatalf("_TOKEN variable leaked into the server: %q", env)
	}
	// The explicit hand-off survives — and replaces, not duplicates, the
	// inherited entry (which the scrub had already dropped here).
	if n := strings.Count(env, "PROBE_API_KEY="); n != 1 {
		t.Fatalf("explicit env must appear exactly once, got %d: %q", n, env)
	}
	if !strings.Contains(env, "PROBE_API_KEY=explicit-key") {
		t.Fatalf("explicit server env was dropped: %q", env)
	}
}

// TestServerEnvOverridesInherited: a name that survives the scrub is replaced
// by the server's env: entry rather than duplicated, so the child cannot read
// the host value instead.
func TestServerEnvOverridesInherited(t *testing.T) {
	got := serverEnv([]string{"PATH=/bin", "PROBE_PLAIN=inherited"}, map[string]string{"PROBE_PLAIN": "explicit"})
	want := []string{"PATH=/bin", "PROBE_PLAIN=explicit"}
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("serverEnv = %v, want %v", got, want)
	}
	if got := serverEnv([]string{"PATH=/bin"}, nil); len(got) != 1 {
		t.Fatalf("nil overrides must pass through: %v", got)
	}
}
