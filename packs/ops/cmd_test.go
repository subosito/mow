package ops

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPeelOpsFlag(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		wantName string
		wantRest []string
	}{
		{"flag before", []string{"--ops", "fleet", "services"}, "fleet", []string{"services"}},
		{"flag after", []string{"services", "--ops", "fleet"}, "fleet", []string{"services"}},
		{"equals", []string{"--ops=fleet", "services"}, "fleet", []string{"services"}},
		{"no flag", []string{"services"}, "", []string{"services"}},
		{"multiple flags", []string{"--ops", "fleet", "--workspace", "."}, "fleet", []string{"--workspace", "."}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotName, gotRest := peelOpsFlag(tt.args)
			if gotName != tt.wantName || len(gotRest) != len(tt.wantRest) {
				t.Fatalf("got (%q, %v) want (%q, %v)", gotName, gotRest, tt.wantName, tt.wantRest)
			}
			for i := range tt.wantRest {
				if gotRest[i] != tt.wantRest[i] {
					t.Fatalf("rest[%d]: got %q want %q", i, gotRest[i], tt.wantRest[i])
				}
			}
		})
	}
}

func TestTakeName(t *testing.T) {
	t.Setenv("MOW_OPS", "")
	// Callers peel the subcommand first, so args[0] (if any) is the profile name.
	name, rest, err := takeName("", []string{"fleet", "extra"})
	if err != nil || name != "fleet" || len(rest) != 1 || rest[0] != "extra" {
		t.Fatalf("got %q %v err=%v", name, rest, err)
	}
	// No positional name: fall back to MOW_OPS, args pass through untouched.
	t.Setenv("MOW_OPS", "env-fleet")
	name, rest, err = takeName("", []string{"--json"})
	if err != nil || name != "env-fleet" || len(rest) != 1 || rest[0] != "--json" {
		t.Fatalf("got %q %v err=%v", name, rest, err)
	}
	// --ops flag beats env when there is no positional name.
	name, _, err = takeName("flag-fleet", []string{"--json"})
	if err != nil || name != "flag-fleet" {
		t.Fatalf("got %q err=%v", name, err)
	}
	// Positional name beats both.
	name, _, err = takeName("flag-fleet", []string{"arg-fleet"})
	if err != nil || name != "arg-fleet" {
		t.Fatalf("got %q err=%v", name, err)
	}
	// Nothing anywhere is an error.
	t.Setenv("MOW_OPS", "")
	if _, _, err := takeName("", nil); err == nil {
		t.Fatal("expected error when no name is available")
	}
}

func TestCmdListProfiles(t *testing.T) {
	root := t.TempDir()
	t.Setenv("MOW_HOME", root)
	// Create two profiles
	for _, name := range []string{"alpha", "beta"} {
		dir := filepath.Join(root, "ops", name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		cfg := "services:\n  - name: svc1\n"
		if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(cfg), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// Run command
	code := cmdListProfiles([]string{})
	if code != 0 {
		t.Fatalf("expected exit 0, got %d", code)
	}
}

func TestCmdListProfilesEmpty(t *testing.T) {
	root := t.TempDir()
	t.Setenv("MOW_HOME", root)
	code := cmdListProfiles([]string{})
	if code != 0 {
		t.Fatalf("expected exit 0, got %d", code)
	}
}

func TestCmdShow(t *testing.T) {
	root := t.TempDir()
	t.Setenv("MOW_HOME", root)
	dir := filepath.Join(root, "ops", "testprof")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := "model: gpt-5-mini\nservices:\n  - name: api\n    logs:\n      - /var/log/api.log\n    actions:\n      restart: [echo, restart]\n      status: [echo, status]\n"
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	code := cmdShow("testprof", []string{})
	if code != 0 {
		t.Fatalf("expected exit 0, got %d", code)
	}
}

func TestCmdShowMissing(t *testing.T) {
	root := t.TempDir()
	t.Setenv("MOW_HOME", root)
	code := cmdShow("missing", []string{})
	if code != 1 {
		t.Fatalf("expected exit 1, got %d", code)
	}
}

func TestCmdCheck(t *testing.T) {
	root := t.TempDir()
	t.Setenv("MOW_HOME", root)
	dir := filepath.Join(root, "ops", "good")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := "services:\n  - name: api\n    logs:\n      - /var/log/api.log\n    actions:\n      restart: [echo, r]\n      status: [echo, s]\n"
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	code := cmdCheck("good", []string{})
	if code != 0 {
		t.Fatalf("expected exit 0, got %d", code)
	}
}

// Missing logs/actions are warnings, not failures: check still exits 0.
func TestCmdCheckMissingLogsWarnsOnly(t *testing.T) {
	root := t.TempDir()
	t.Setenv("MOW_HOME", root)
	dir := filepath.Join(root, "ops", "bad")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := "services:\n  - name: api\n    actions:\n      restart: [echo, r]\n"
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	code := cmdCheck("bad", []string{})
	if code != 0 {
		t.Fatalf("expected exit 0 (warnings only), got %d", code)
	}
}

// Hard errors (no services, dangling acp peer) exit 1.
func TestCmdCheckHardErrors(t *testing.T) {
	root := t.TempDir()
	t.Setenv("MOW_HOME", root)
	for _, tc := range []struct{ name, cfg string }{
		{"empty", "services: []\n"},
		{"dangling-acp", "services:\n  - name: api\n    logs: [/var/log/api.log]\n    acp: ghost\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := filepath.Join(root, "ops", tc.name)
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(tc.cfg), 0o644); err != nil {
				t.Fatal(err)
			}
			if code := cmdCheck(tc.name, []string{}); code != 1 {
				t.Fatalf("expected exit 1, got %d", code)
			}
		})
	}
}

func TestCmdServices(t *testing.T) {
	root := t.TempDir()
	t.Setenv("MOW_HOME", root)
	dir := filepath.Join(root, "ops", "svcprof")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := "services:\n  - name: api\n    logs:\n      - /var/log/api.log\n    actions:\n      restart: [echo, r]\n      status: [echo, s]\n"
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	code := cmdServices("svcprof", []string{})
	if code != 0 {
		t.Fatalf("expected exit 0, got %d", code)
	}
}

func TestCmdIncidents(t *testing.T) {
	root := t.TempDir()
	t.Setenv("MOW_HOME", root)
	dir := filepath.Join(root, "ops", "incprof")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := "services:\n  - name: api\n"
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	// Create incidents dir
	incDir := filepath.Join(dir, "incidents")
	if err := os.MkdirAll(incDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Create an incident
	inc := `{"id":"test-1","signature":"sig-1","summary":"test","status":"open"}`
	if err := os.WriteFile(filepath.Join(incDir, "test-1.json"), []byte(inc), 0o644); err != nil {
		t.Fatal(err)
	}
	code := cmdIncidents("incprof", []string{})
	if code != 0 {
		t.Fatalf("expected exit 0, got %d", code)
	}
}

func TestFirstNonEmpty(t *testing.T) {
	if got := firstNonEmpty("", " ", "keep", ""); got != "keep" {
		t.Errorf("firstNonEmpty = %q, want keep", got)
	}
	if got := firstNonEmpty("", "", ""); got != "" {
		t.Errorf("firstNonEmpty = %q, want empty", got)
	}
}

func TestTruncateLog(t *testing.T) {
	if got := truncateLog("short", 10); got != "short" {
		t.Errorf("truncateLog(short) = %q", got)
	}
	long := strings.Repeat("x", 20)
	// truncateLog appends a multi-byte ellipsis, so length is n + len("…").
	if got, want := truncateLog(long, 10), strings.Repeat("x", 10)+"…"; got != want {
		t.Errorf("truncateLog(long) = %q, want %q", got, want)
	}
}

func TestDefaultOpsRunPrompt(t *testing.T) {
	prompt := defaultOpsRunPrompt("test-fleet")
	if !strings.Contains(prompt, "test-fleet") {
		t.Fatalf("prompt missing profile name: %s", prompt)
	}
	if !strings.Contains(prompt, "ops_incident list") {
		t.Fatalf("prompt missing workflow: %s", prompt)
	}
	if !strings.Contains(prompt, "delegate to the service's peer") {
		t.Fatalf("prompt missing delegate: %s", prompt)
	}
	if strings.Contains(prompt, "acp_delegate") {
		t.Fatalf("stale acp_delegate in prompt: %s", prompt)
	}
}
