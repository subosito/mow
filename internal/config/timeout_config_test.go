package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/subosito/mow/internal/config"
)

// TestTimeoutKnobValidation proves llm.first_byte_timeout_sec and
// llm.call_timeout_sec accept 0 (default sentinel) and positive values but
// reject negatives at load time.
func TestTimeoutKnobValidation(t *testing.T) {
	for _, tc := range []struct {
		name    string
		yaml    string
		wantErr string // substring; empty = must load
		fb, ct  int    // expected values when wantErr == ""
	}{
		{"omitted", "llm: {}", "", 0, 0},
		{"explicit zero", "llm:\n  first_byte_timeout_sec: 0\n  call_timeout_sec: 0\n", "", 0, 0},
		{"positive", "llm:\n  first_byte_timeout_sec: 600\n  call_timeout_sec: 45\n", "", 600, 45},
		{"negative first byte", "llm:\n  first_byte_timeout_sec: -1\n", "first_byte_timeout_sec", 0, 0},
		{"negative call", "llm:\n  call_timeout_sec: -120\n", "call_timeout_sec", 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(config.EnvHome, t.TempDir())
			path := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(path, []byte(tc.yaml), 0o644); err != nil {
				t.Fatal(err)
			}
			f, err := config.Load(path)
			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("want error containing %q, got nil", tc.wantErr)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("error %q must mention %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected load error: %v", err)
			}
			if f.LLM.FirstByteTimeoutSec != tc.fb || f.LLM.CallTimeoutSec != tc.ct {
				t.Fatalf("got (%d,%d), want (%d,%d)",
					f.LLM.FirstByteTimeoutSec, f.LLM.CallTimeoutSec, tc.fb, tc.ct)
			}
		})
	}
}

// TestTimeoutKnobsProjectRestricted proves a trusted project config cannot
// set the network timeout knobs — they are host/user behavior, same trust
// class as llm.base_url.
func TestTimeoutKnobsProjectRestricted(t *testing.T) {
	t.Setenv(config.EnvHome, t.TempDir())
	t.Setenv("MOW_TRUST_PROJECT", "1")
	t.Setenv("OPENAI_API_KEY", "sk-test")
	for _, k := range []string{"OPENAI_BASE_URL", "MOW_API_KEY", "MOW_MODEL", "MOW_BASE_URL", "ANTHROPIC_BASE_URL", "MOW_WIRE"} {
		t.Setenv(k, "")
	}
	ws := t.TempDir()
	if err := os.MkdirAll(filepath.Join(ws, ".mow"), 0o755); err != nil {
		t.Fatal(err)
	}
	project := "llm:\n  first_byte_timeout_sec: 9999\n  call_timeout_sec: 9999\n"
	if err := os.WriteFile(filepath.Join(ws, ".mow", "config.yaml"), []byte(project), 0o644); err != nil {
		t.Fatal(err)
	}
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(ws); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(old) })

	f, err := config.Load()
	if err != nil {
		t.Fatalf("load with trusted project config: %v", err)
	}
	if f.LLM.FirstByteTimeoutSec != 0 || f.LLM.CallTimeoutSec != 0 {
		t.Fatalf("project config must not set timeout knobs, got (%d,%d)",
			f.LLM.FirstByteTimeoutSec, f.LLM.CallTimeoutSec)
	}
}
