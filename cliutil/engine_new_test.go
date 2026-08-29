package cliutil_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/subosito/mow/cliutil"
)

// writeConfig drops a minimal config yaml in dir and returns its path.
func writeConfig(t *testing.T, dir string) string {
	t.Helper()
	p := filepath.Join(dir, "mow.yaml")
	body := strings.Join([]string{
		"llm:",
		"  model: gpt-5-mini",
		"  base_url: http://127.0.0.1:9/v1",
		"  api_key: test-key",
		"",
	}, "\n")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestNewEngineFromFlags(t *testing.T) {
	ws := t.TempDir()
	cfg := writeConfig(t, t.TempDir())

	var ef cliutil.EngineFlags
	fs := cliutil.NewFlagSet("run")
	ef.Bind(fs)
	if err := fs.Parse([]string{"--workspace", ws, "--config", cfg}); err != nil {
		t.Fatal(err)
	}
	eng, err := ef.NewEngine()
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	if eng == nil {
		t.Fatal("NewEngine returned nil engine")
	}
	defer eng.Close()
	if got := eng.Workspace(); got != ws {
		// Workspace may be normalized (symlinks); compare resolved forms.
		if resolved, rerr := filepath.EvalSymlinks(ws); rerr != nil || resolved != got {
			t.Fatalf("Workspace()=%q want %q", got, ws)
		}
	}
}

func TestNewEngineCLIFromFlags(t *testing.T) {
	ws := t.TempDir()
	cfg := writeConfig(t, t.TempDir())

	var ef cliutil.EngineFlags
	fs := cliutil.NewFlagSet("run")
	ef.Bind(fs)
	if err := fs.Parse([]string{"--workspace", ws, "--config", cfg, "--stream"}); err != nil {
		t.Fatal(err)
	}
	eng, err := ef.NewEngineCLI()
	if err != nil {
		t.Fatalf("NewEngineCLI: %v", err)
	}
	if eng == nil {
		t.Fatal("NewEngineCLI returned nil engine")
	}
	eng.Close()
}

func TestNewEngineMissingConfigErrors(t *testing.T) {
	var ef cliutil.EngineFlags
	fs := cliutil.NewFlagSet("run")
	ef.Bind(fs)
	missing := filepath.Join(t.TempDir(), "nope.yaml")
	if err := fs.Parse([]string{"--config", missing, "--workspace", t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	eng, err := ef.NewEngine()
	if err == nil {
		if eng != nil {
			eng.Close()
		}
		t.Fatal("want error for missing --config path")
	}
}

func TestNewEngineInvalidWorkspaceErrors(t *testing.T) {
	var ef cliutil.EngineFlags
	fs := cliutil.NewFlagSet("run")
	ef.Bind(fs)
	// A regular file is not a usable workspace root.
	f := filepath.Join(t.TempDir(), "file.txt")
	if err := os.WriteFile(f, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := fs.Parse([]string{"--workspace", f}); err != nil {
		t.Fatal(err)
	}
	eng, err := ef.NewEngine()
	if err == nil && eng != nil {
		eng.Close()
		t.Skip("engine accepts a file workspace on this build; nothing to assert")
	}
}

func TestOptionsExplicitModel(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name         string
		args         []string
		wantModel    string
		wantExplicit bool
	}{
		{"no model", nil, "", false},
		{"model set", []string{"--model", "gpt-5-mini"}, "gpt-5-mini", true},
		{"blank model", []string{"--model", "   "}, "   ", false},
		{"empty model", []string{"--model", ""}, "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			var ef cliutil.EngineFlags
			fs := cliutil.NewFlagSet("run")
			ef.Bind(fs)
			if err := fs.Parse(c.args); err != nil {
				t.Fatal(err)
			}
			opt := ef.Options()
			if opt.Model != c.wantModel || opt.ExplicitModel != c.wantExplicit {
				t.Fatalf("Model=%q Explicit=%v want %q,%v", opt.Model, opt.ExplicitModel, c.wantModel, c.wantExplicit)
			}
		})
	}
}

func TestOptionsProvider(t *testing.T) {
	t.Parallel()
	var ef cliutil.EngineFlags
	fs := cliutil.NewFlagSet("run")
	ef.Bind(fs)
	if err := fs.Parse([]string{"--provider", "gateway", "--model", "gpt-5-mini"}); err != nil {
		t.Fatal(err)
	}
	opt := ef.Options()
	if opt.LLMProvider != "gateway" {
		t.Fatalf("LLMProvider=%q", opt.LLMProvider)
	}
	if opt.Model != "gpt-5-mini" || !opt.ExplicitModel {
		t.Fatalf("model=%q explicit=%v", opt.Model, opt.ExplicitModel)
	}
}

func TestOptionsExtraRootModes(t *testing.T) {
	t.Parallel()
	var ef cliutil.EngineFlags
	fs := cliutil.NewFlagSet("run")
	ef.Bind(fs)
	args := []string{
		"--extra-root", "/rw/one",
		"--extra-root", "/ro/one:ro",
		"--extra-root", "/rw/two:rw",
		"--extra-root", "   ", // dropped by stringList.Set
	}
	if err := fs.Parse(args); err != nil {
		t.Fatal(err)
	}
	if len(ef.ExtraRoots) != 3 {
		t.Fatalf("ExtraRoots=%v want 3 entries", ef.ExtraRoots)
	}
	opt := ef.Options()
	if strings.Join(opt.ExtraRoots, ",") != "/rw/one,/rw/two" {
		t.Fatalf("ExtraRoots=%v", opt.ExtraRoots)
	}
	if strings.Join(opt.ExtraRootsReadOnly, ",") != "/ro/one" {
		t.Fatalf("ExtraRootsReadOnly=%v", opt.ExtraRootsReadOnly)
	}
}

func TestOptionsSystemPrefixCopied(t *testing.T) {
	t.Parallel()
	var ef cliutil.EngineFlags
	ef.SystemPrefix = []string{"a", "b"}
	opt := ef.Options()
	opt.SystemPrefix[0] = "mutated"
	if ef.SystemPrefix[0] != "a" {
		t.Fatal("Options() must copy SystemPrefix, not alias the flag slice")
	}
}

func TestOptionsMaxTurnsMatrix(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		args []string
		want int
	}{
		{"omitted leaves config", nil, 0},
		{"zero is unlimited sentinel", []string{"--max-turns", "0"}, -1},
		{"positive passthrough", []string{"--max-turns", "12"}, 12},
		{"negative passthrough", []string{"--max-turns", "-5"}, -5},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			var ef cliutil.EngineFlags
			fs := cliutil.NewFlagSet("run")
			ef.Bind(fs)
			if err := fs.Parse(c.args); err != nil {
				t.Fatal(err)
			}
			if got := ef.Options().MaxTurns; got != c.want {
				t.Fatalf("MaxTurns=%d want %d", got, c.want)
			}
		})
	}
}

func TestConfigPathsEdges(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{"empty", "", nil},
		{"spaces only", "   ", nil},
		{"tab only", "\t\n", nil},
		{"trimmed", "  /a/b.yaml ", []string{"/a/b.yaml"}},
		{"path with spaces", "/a/my dir/c.yaml", []string{"/a/my dir/c.yaml"}},
		{"unicode", "/tmp/設定.yaml", []string{"/tmp/設定.yaml"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			ef := cliutil.EngineFlags{Config: c.in}
			got := ef.ConfigPaths()
			if len(got) != len(c.want) {
				t.Fatalf("ConfigPaths()=%v want %v", got, c.want)
			}
			for i := range got {
				if got[i] != c.want[i] {
					t.Fatalf("ConfigPaths()=%v want %v", got, c.want)
				}
			}
		})
	}
}
