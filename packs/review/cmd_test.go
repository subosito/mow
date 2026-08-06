package review

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/subosito/mow/ext"
)

// parseFlags binds the review surface exactly like runCommand does and parses
// args, so the tests exercise the real flag wiring rather than a copy of it.
func parseFlags(t *testing.T, cmd string, args ...string) (CLIFlags, []string, error) {
	t.Helper()
	fs := flag.NewFlagSet("mow "+cmd, flag.ContinueOnError)
	fs.SetOutput(new(strings.Builder))
	var rf CLIFlags
	rf.Bind(fs)
	err := fs.Parse(args)
	return rf, fs.Args(), err
}

// mustProfileFor wraps the production command→persona mapping for tests that
// only care about the happy path. Using the real profileFor (rather than a
// mirror of it) is deliberate: a mirror silently keeps passing when the
// production mapping gains a command.
func mustProfileFor(t *testing.T, cmd string) *Profile {
	t.Helper()
	prof, err := profileFor(cmd)
	if err != nil {
		t.Fatalf("profileFor(%q): %v", cmd, err)
	}
	return prof
}

func resolveFlags(t *testing.T, cmd string, args ...string) (Request, Format, ExitPolicy) {
	t.Helper()
	rf, paths, err := parseFlags(t, cmd, args...)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	req, format, policy, err := rf.Resolve(mustProfileFor(t, cmd), "/ws", paths)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	return req, format, policy
}

// `mow sec` must use the internal security profile (stricter default floor).
func TestSecPinsSecurityProfile(t *testing.T) {
	req, format, policy := resolveFlags(t, "sec")
	if req.Profile.Name != "security" {
		t.Fatalf("profile = %q, want security", req.Profile.Name)
	}
	if req.MinSeverity != req.Profile.MinSeverity {
		t.Fatalf("min severity = %v, want profile default %v", req.MinSeverity, req.Profile.MinSeverity)
	}
	if format != FormatText {
		t.Fatalf("format = %q, want text", format)
	}
	if policy.FailOn != 0 {
		t.Fatalf("FailOn = %v, want unset (profile default applies at exit)", policy.FailOn)
	}
}

// --profile is not a public flag on either command (profile is internal), and
// the persona comes from the command name alone.
func TestCommandsRejectProfileFlag(t *testing.T) {
	if _, _, err := parseFlags(t, "sec", "--profile", "general"); err == nil {
		t.Fatal("mow sec --profile should be rejected")
	}
	if _, _, err := parseFlags(t, "review", "--profile", "security"); err == nil {
		t.Fatal("mow review --profile should be rejected")
	}
	// There is no flag-struct field that could disagree with the command, so
	// identical flags must still yield different personas.
	rf, paths, err := parseFlags(t, "review")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	gen, _, _, err := rf.Resolve(mustProfileFor(t, "review"), "/ws", paths)
	if err != nil {
		t.Fatalf("resolve review: %v", err)
	}
	sec, _, _, err := rf.Resolve(mustProfileFor(t, "sec"), "/ws", paths)
	if err != nil {
		t.Fatalf("resolve sec: %v", err)
	}
	if gen.Profile.Name != "general" || sec.Profile.Name != "security" {
		t.Fatalf("persona must follow the command: got %q and %q", gen.Profile.Name, sec.Profile.Name)
	}
}

// An unregistered command must fail loudly rather than silently inheriting the
// general review: a future "mow audit" that forgets its persona would
// otherwise run a plain code review under a security-sounding name.
func TestRunCommandRejectsUnknownCommand(t *testing.T) {
	if got := runCommand("audit", []string{"--quiet"}); got != ExitError {
		t.Fatalf("unknown command exit = %d, want %d", got, ExitError)
	}
}

// A nil profile is the general review, so a library caller cannot accidentally
// produce a report with no persona at all.
func TestResolveNilProfileDefaultsToGeneral(t *testing.T) {
	rf, paths, err := parseFlags(t, "review")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	req, _, _, err := rf.Resolve(nil, "/ws", paths)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if req.Profile == nil || req.Profile.Name != "general" {
		t.Fatalf("nil profile should default to general, got %+v", req.Profile)
	}
}

func TestReviewDefaultsToGeneralProfile(t *testing.T) {
	req, _, _ := resolveFlags(t, "review")
	if req.Profile.Name != "general" {
		t.Fatalf("profile = %q, want general", req.Profile.Name)
	}
}

func TestResolveRejectsBadValues(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"format", []string{"--format", "yaml"}, "unknown format"},
		{"budget", []string{"--budget", "huge"}, "unknown budget"},
		{"min-severity", []string{"--min-severity", "spicy"}, "unknown --min-severity"},
		{"fail-on", []string{"--fail-on", "spicy"}, "unknown --fail-on"},
		{"diff+staged", []string{"--diff", "a...b", "--staged"}, "mutually exclusive"},
		{"diff+base", []string{"--diff", "a...b", "--base", "main"}, "mutually exclusive"},
		{"staged+base", []string{"--staged", "--base", "main"}, "mutually exclusive"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rf, paths, err := parseFlags(t, "review", tc.args...)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			_, _, _, err = rf.Resolve(mustProfileFor(t, "review"), "/ws", paths)
			if err == nil {
				t.Fatalf("expected error for %v", tc.args)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %q, want mention of %q", err, tc.want)
			}
		})
	}
}

// Paths plus a ref selector is ambiguous: reviewing "the diff" and "these
// files" are different scopes and silently picking one hides what ran.
func TestResolveRejectsPathsWithSelector(t *testing.T) {
	rf, _, err := parseFlags(t, "review", "--staged")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if _, _, _, err := rf.Resolve(mustProfileFor(t, "review"), "/ws", []string{"./internal"}); err == nil {
		t.Fatal("paths + --staged should be rejected")
	}
}

func TestResolveCarriesScopeSelectors(t *testing.T) {
	req, _, _ := resolveFlags(t, "review", "--base", "origin/main",
		"--exclude", "vendor/**", "--exclude", "testdata/**",
		"--budget", "large", "--include-all")
	sc := req.Scope
	if sc.Base != "origin/main" || sc.Budget != "large" || !sc.IncludeAll {
		t.Fatalf("scope not carried: %+v", sc)
	}
	if len(sc.Excludes) != 2 || sc.Excludes[0] != "vendor/**" {
		t.Fatalf("excludes = %v", sc.Excludes)
	}
	if sc.Workspace != "/ws" {
		t.Fatalf("workspace = %q", sc.Workspace)
	}
}

func TestResolvePositionalPaths(t *testing.T) {
	req, _, _ := resolveFlags(t, "review", "./internal/api", "main.go")
	if got := req.Scope.Paths; len(got) != 2 || got[0] != "./internal/api" || got[1] != "main.go" {
		t.Fatalf("paths = %v", got)
	}
}

// --include-low lowers the floor, but an explicit --min-severity wins: the more
// specific flag must not be silently overridden.
func TestMinSeverityPrecedence(t *testing.T) {
	req, _, _ := resolveFlags(t, "sec", "--include-low")
	if req.MinSeverity != SevInfo {
		t.Fatalf("--include-low min = %v, want info", req.MinSeverity)
	}
	req, _, _ = resolveFlags(t, "sec", "--include-low", "--min-severity", "high")
	if req.MinSeverity != SevHigh {
		t.Fatalf("explicit --min-severity lost: %v", req.MinSeverity)
	}
}

func TestResolveExitPolicyAndPasses(t *testing.T) {
	req, format, policy := resolveFlags(t, "review",
		"--fail-on", "medium", "--exit-zero", "--format", "sarif",
		"--include-unverified", "--no-verify")
	if policy.FailOn != SevMedium || !policy.ExitZero {
		t.Fatalf("policy = %+v", policy)
	}
	if format != FormatSARIF {
		t.Fatalf("format = %q", format)
	}
	if !req.IncludeUnverified || !req.SkipVerification {
		t.Fatalf("pass flags lost: %+v", req)
	}
}

// The whole point of the command layer is a consumable artifact: --output must
// contain the chosen format verbatim, with no ANSI color and no progress text.
func TestEmitWritesFileInFormat(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "report.json")
	rf := CLIFlags{Output: path, Quiet: true}
	if err := emit(sampleReport("general"), FormatJSON, rf, false); err != nil {
		t.Fatalf("emit: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if strings.Contains(string(data), "\x1b[") {
		t.Fatal("file output must never contain ANSI color")
	}
	var got map[string]any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("output is not valid JSON: %v", err)
	}
	if got["profile"] != "general" {
		t.Fatalf("profile = %v", got["profile"])
	}
	if _, ok := got["findings"]; !ok {
		t.Fatal("findings key missing from JSON artifact")
	}
}

// A bad --output path must be a clear error, not a silently dropped report.
func TestEmitBadOutputPath(t *testing.T) {
	rf := CLIFlags{Output: filepath.Join(t.TempDir(), "no-such-dir", "r.json"), Quiet: true}
	err := emit(sampleReport("general"), FormatJSON, rf, false)
	if err == nil {
		t.Fatal("expected error for unwritable --output")
	}
	if !strings.Contains(err.Error(), "--output") {
		t.Fatalf("error should name --output: %v", err)
	}
}

// --verbose (engine flag) is what turns on notes/excluded files; unrelated
// filter flags must not smuggle verbosity in.
func TestEmitVerboseComesFromEngineFlag(t *testing.T) {
	dir := t.TempDir()
	rep := sampleReport("general")

	quiet := filepath.Join(dir, "quiet.txt")
	if err := emit(rep, FormatText, CLIFlags{Output: quiet, Quiet: true, IncludeLow: true, Unverified: true}, false); err != nil {
		t.Fatalf("emit: %v", err)
	}
	loud := filepath.Join(dir, "loud.txt")
	if err := emit(rep, FormatText, CLIFlags{Output: loud, Quiet: true}, true); err != nil {
		t.Fatalf("emit: %v", err)
	}

	quietOut := readFileString(t, quiet)
	loudOut := readFileString(t, loud)
	if strings.Contains(quietOut, "vendor/x.go") {
		t.Fatal("excluded files leaked into non-verbose output")
	}
	if !strings.Contains(loudOut, "vendor/x.go") {
		t.Fatal("--verbose should list excluded files")
	}
}

func readFileString(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

func TestResolveWorkspaceDefaultsToCwd(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	got, err := resolveWorkspace("  ")
	if err != nil {
		t.Fatalf("resolveWorkspace: %v", err)
	}
	if got != cwd {
		t.Fatalf("workspace = %q, want %q", got, cwd)
	}
	got, err = resolveWorkspace("./ext")
	if err != nil {
		t.Fatalf("resolveWorkspace: %v", err)
	}
	if !filepath.IsAbs(got) {
		t.Fatalf("workspace %q should be absolute", got)
	}
}

// Both commands must be registered, or the pack silently does nothing.
func TestCommandsRegistered(t *testing.T) {
	for _, name := range []string{"review", "sec"} {
		c, ok := ext.LookupCommand(name)
		if !ok {
			t.Fatalf("command %q not registered", name)
		}
		if c.Run == nil {
			t.Fatalf("command %q has no Run", name)
		}
		if !strings.Contains(strings.ToLower(c.Summary), "advisory") {
			t.Fatalf("command %q summary should say it is advisory: %q", name, c.Summary)
		}
	}
}

// scope.diff must carry a git range only when one was used. Reporting the
// path list as a "diff" made JSON consumers show a range that never existed.
func TestScopeInfoDiffOnlyForRanges(t *testing.T) {
	cases := []struct {
		mode     string
		selector string
		wantDiff string
	}{
		{"diff", "main...HEAD", "main...HEAD"},
		{"base", "origin/main...HEAD", "origin/main...HEAD"},
		{"paths", "ext/review/exit.go", ""},
		{"worktree", "uncommitted changes", ""},
		{"staged", "staged changes", ""},
	}
	for _, tc := range cases {
		t.Run(tc.mode, func(t *testing.T) {
			sc := &Scope{Mode: tc.mode, Selector: tc.selector, Budget: mediumBudgetForTest()}
			info := sc.Info(ScopeRequest{})
			if info.Diff != tc.wantDiff {
				t.Fatalf("Diff = %q, want %q", info.Diff, tc.wantDiff)
			}
			if info.Mode != tc.mode {
				t.Fatalf("Mode = %q, want %q", info.Mode, tc.mode)
			}
			// Selection is always populated so a consumer never has to guess.
			if info.Selection != tc.selector {
				t.Fatalf("Selection = %q, want %q", info.Selection, tc.selector)
			}
			if got := scopeSelectorLine(info); got != tc.selector {
				t.Fatalf("text selector line = %q, want %q", got, tc.selector)
			}
		})
	}
}

func mediumBudgetForTest() Budget {
	b, _ := LookupBudget("medium")
	return b
}

// Findings from the dogfood review of this package (mow review on its own
// code). Each case below is a bug the reviewer found and this test pins.

// The help pre-scan used to inspect every argument, so `mow review -- --help`
// (a file literally named "--help") printed usage and exited 0 — which in CI
// is indistinguishable from a clean review. It also matched flag *values*.
func TestWantsHelp(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want bool
	}{
		{"no args", nil, false},
		{"short", []string{"-h"}, true},
		{"long", []string{"--help"}, true},
		{"stdlib spelling", []string{"-help"}, true},
		{"bare word first", []string{"help"}, true},
		{"help after flags", []string{"--staged", "--help"}, true},
		// "--" ends flag parsing: what follows is a path, not a help request.
		{"escaped path named --help", []string{"--", "--help"}, false},
		{"escaped path named help", []string{"--", "help"}, false},
		// A flag value that spells a help token must not trigger usage.
		{"value named help", []string{"--diff", "help"}, false},
		{"path named help", []string{"./pkg", "help"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := wantsHelp(tc.args); got != tc.want {
				t.Fatalf("wantsHelp(%v) = %v, want %v", tc.args, got, tc.want)
			}
		})
	}
}

// A failed render must not leave a truncated artifact, and must not destroy a
// previous good report at that path: CI publishes the path regardless.
func TestEmitDoesNotClobberOnRenderFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "report.json")
	const previous = `{"schema_version":1,"previous":"good report"}`
	if err := os.WriteFile(path, []byte(previous), 0o644); err != nil {
		t.Fatal(err)
	}

	// An unknown format makes Render fail after the output file is opened.
	err := emit(sampleReport("general"), Format("no-such-format"), CLIFlags{Output: path, Quiet: true}, false)
	if err == nil {
		t.Fatal("expected a render error")
	}
	got, rerr := os.ReadFile(path)
	if rerr != nil {
		t.Fatalf("previous report was destroyed: %v", rerr)
	}
	if string(got) != previous {
		t.Fatalf("previous report was overwritten:\n%s", got)
	}
	// No temp files left behind.
	entries, _ := os.ReadDir(filepath.Dir(path))
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".mow-report-") {
			t.Fatalf("temp file left behind: %s", e.Name())
		}
	}
}

// The successful path must still produce a complete, readable artifact.
func TestEmitAtomicWriteProducesReadableFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "report.json")
	if err := emit(sampleReport("general"), FormatJSON, CLIFlags{Output: path, Quiet: true}, false); err != nil {
		t.Fatalf("emit: %v", err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	// Readable by other tools, not CreateTemp's 0600.
	if perm := fi.Mode().Perm(); perm&0o044 == 0 {
		t.Fatalf("report mode = %v, want world/group readable", perm)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var rep map[string]any
	if err := json.Unmarshal(data, &rep); err != nil {
		t.Fatalf("artifact is not complete JSON: %v", err)
	}
}

// Overwriting an existing report must replace it wholesale, with no leftovers
// from the longer previous content.
func TestEmitOverwritesCleanly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "report.json")
	long := strings.Repeat(`{"filler":"x"}`, 500)
	if err := os.WriteFile(path, []byte(long), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := emit(sampleReport("general"), FormatJSON, CLIFlags{Output: path, Quiet: true}, false); err != nil {
		t.Fatalf("emit: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "filler") {
		t.Fatal("old content survived the overwrite")
	}
	var rep map[string]any
	if err := json.Unmarshal(data, &rep); err != nil {
		t.Fatalf("artifact is not valid JSON: %v", err)
	}
}
