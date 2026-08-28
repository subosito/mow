package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/subosito/mow/internal/config"
)

// Baseline perf harness for the startup path: config Load with a realistic
// user config file, and the no-file defaults path.

const benchYAML = `llm:
  wire: openai-chat-completions
  base_url: http://127.0.0.1:PORT/v1
  api_key_env: OPENAI_API_KEY
  model: gpt-5-mini
  stream: true
tools:
  enable: [read, glob, grep, write, edit, bash]
policy:
  max_turns: 120
  bash_timeout_sec: 300
  max_bash_timeout_sec: 900
  max_read_bytes: 524288
  max_tool_result_chars: 24000
  max_parallel_tools: 8
session:
  dir: ""
skills:
  dirs: []
extensions:
  acp:
    peers:
      - name: peer-agent
        command: [peer-agent, agent, stdio]
        timeout_sec: 600
`

func benchConfigFile(b *testing.B) string {
	b.Helper()
	dir := b.TempDir()
	p := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(p, []byte(benchYAML), 0o600); err != nil {
		b.Fatal(err)
	}
	return p
}

// BenchmarkConfigLoadWithFile: user config present — yaml parse + merge + normalize.
func BenchmarkConfigLoadWithFile(b *testing.B) {
	b.Setenv(config.EnvHome, b.TempDir())
	b.Setenv("OPENAI_API_KEY", "sk-test")
	b.Setenv("OPENAI_MODEL", "m")
	p := benchConfigFile(b)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		if _, err := config.Load(p); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkConfigLoadDefaults: no config files — defaults + env only.
func BenchmarkConfigLoadDefaults(b *testing.B) {
	b.Setenv(config.EnvHome, b.TempDir())
	b.Setenv("OPENAI_API_KEY", "sk-test")
	b.Setenv("OPENAI_MODEL", "m")
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		if _, err := config.Load(); err != nil {
			b.Fatal(err)
		}
	}
}
