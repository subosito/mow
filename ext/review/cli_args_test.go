package review

import (
	"flag"
	"strings"
	"testing"

	"github.com/subosito/mow/cliutil"
)

// newArgFlagSet builds the same flag surface runCommand uses, so permutation
// is tested against the real bool/value mix (including engine flags).
func newArgFlagSet() *flag.FlagSet {
	fs := flag.NewFlagSet("mow review", flag.ContinueOnError)
	fs.SetOutput(new(strings.Builder))
	var rf CLIFlags
	rf.Bind(fs)
	rf.BindProfile(fs)
	var ef cliutil.EngineFlags
	ef.Bind(fs)
	return fs
}

func TestParseArgsPermutation(t *testing.T) {
	cases := []struct {
		name  string
		args  []string
		paths []string
	}{
		{"flags only", []string{"--staged"}, nil},
		{"paths only", []string{"./a", "b.go"}, []string{"./a", "b.go"}},
		{"flags before paths", []string{"--no-verify", "./a"}, []string{"./a"}},
		// The regression: a bool flag written after a path used to be swallowed
		// as a filename.
		{"bool flag after path", []string{"./a", "--no-verify"}, []string{"./a"}},
		{"value flag after path", []string{"./a", "--format", "json"}, []string{"./a"}},
		{"value flag with equals after path", []string{"./a", "--format=json"}, []string{"./a"}},
		{"interleaved", []string{"--staged", "./a", "--format", "json", "b.go"}, []string{"./a", "b.go"}},
		{"bool then path must keep path", []string{"--staged", "./a"}, []string{"./a"}},
		{"single dash flag", []string{"./a", "-no-verify"}, []string{"./a"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fs := newArgFlagSet()
			paths, err := parseArgs(fs, tc.args)
			if err != nil {
				t.Fatalf("parseArgs(%v): %v", tc.args, err)
			}
			if len(paths) != len(tc.paths) {
				t.Fatalf("paths = %v, want %v", paths, tc.paths)
			}
			for i := range paths {
				if paths[i] != tc.paths[i] {
					t.Fatalf("paths = %v, want %v", paths, tc.paths)
				}
			}
		})
	}
}

// Trailing flags must actually take effect, not merely be removed from paths.
func TestParseArgsAppliesTrailingFlags(t *testing.T) {
	fs := flag.NewFlagSet("mow review", flag.ContinueOnError)
	fs.SetOutput(new(strings.Builder))
	var rf CLIFlags
	rf.Bind(fs)
	rf.BindProfile(fs)

	paths, err := parseArgs(fs, []string{"./internal", "--no-verify", "--format", "sarif", "--min-severity=high"})
	if err != nil {
		t.Fatalf("parseArgs: %v", err)
	}
	if len(paths) != 1 || paths[0] != "./internal" {
		t.Fatalf("paths = %v", paths)
	}
	if !rf.NoVerify {
		t.Fatal("--no-verify after a path was not applied")
	}
	if rf.Format != "sarif" {
		t.Fatalf("Format = %q, want sarif", rf.Format)
	}
	if rf.MinSeverity != "high" {
		t.Fatalf("MinSeverity = %q, want high", rf.MinSeverity)
	}
}

// "--" is the escape hatch for paths that begin with a dash.
func TestParseArgsDoubleDash(t *testing.T) {
	fs := newArgFlagSet()
	paths, err := parseArgs(fs, []string{"--staged", "--", "-weird-name.go"})
	if err != nil {
		t.Fatalf("parseArgs: %v", err)
	}
	if len(paths) != 1 || paths[0] != "-weird-name.go" {
		t.Fatalf("paths = %v, want [-weird-name.go]", paths)
	}
}

// A repeatable flag after a path must still accumulate.
func TestParseArgsRepeatableAfterPath(t *testing.T) {
	fs := flag.NewFlagSet("mow review", flag.ContinueOnError)
	fs.SetOutput(new(strings.Builder))
	var rf CLIFlags
	rf.Bind(fs)
	if _, err := parseArgs(fs, []string{"./a", "--exclude", "vendor/**", "--exclude", "testdata/**"}); err != nil {
		t.Fatalf("parseArgs: %v", err)
	}
	if len(rf.Excludes) != 2 {
		t.Fatalf("excludes = %v, want 2", rf.Excludes)
	}
}

// An unknown flag must still be an error, not silently treated as a path.
func TestParseArgsUnknownFlagErrors(t *testing.T) {
	fs := newArgFlagSet()
	if _, err := parseArgs(fs, []string{"./a", "--not-a-flag"}); err == nil {
		t.Fatal("unknown flag should error")
	}
}
