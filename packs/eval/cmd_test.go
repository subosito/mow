package eval

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/subosito/mow"
	x "github.com/subosito/mow/eval"
)

// run is the package-level command entry registered as `mow eval`. Calling it
// directly exercises arg parsing + fixture loading + exit codes without a real
// process. It must not make network calls on the script path.

func TestRunHelpReturnsUsageExit(t *testing.T) {
	// No args and -h/--help all map to usage (exit 2).
	for _, args := range [][]string{nil, {"-h"}, {"--help"}} {
		if code := run(args); code != 2 {
			t.Fatalf("args=%v: want exit 2, got %d", args, code)
		}
	}
}

func TestRunUnknownSubcommandReturns2(t *testing.T) {
	if code := run([]string{"nope"}); code != 2 {
		t.Fatalf("unknown subcommand: want exit 2, got %d", code)
	}
}

func TestRunRunNoFixtureReturns2(t *testing.T) {
	// `mow eval run` with no fixture path → usage error.
	if code := run([]string{"run"}); code != 2 {
		t.Fatalf("no fixture: want exit 2, got %d", code)
	}
}

func TestRunRunMissingFixtureFileReturns1(t *testing.T) {
	if code := run([]string{"run", filepath.Join(t.TempDir(), "absent.json")}); code != 1 {
		t.Fatalf("missing fixture file: want exit 1, got %d", code)
	}
}

func TestRunRunMalformedJSONReturns1(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if code := run([]string{"run", path}); code != 1 {
		t.Fatalf("malformed json: want exit 1, got %d", code)
	}
}

func TestRunRunScriptedPassReturns0(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ok.json")
	// A scripted case with no API: the script satisfies Expect.Contains.
	raw, _ := json.Marshal(x.Fixture{
		Name: "ok-suite",
		Cases: []x.Case{{
			Name:   "says-pong",
			Prompt: "ping",
			Script: []mow.Message{{Role: "assistant", Content: "pong"}},
			Expect: x.Expect{Contains: []string{"pong"}},
		}},
	})
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if code := run([]string{"run", path}); code != 0 {
		t.Fatalf("scripted pass: want exit 0, got %d", code)
	}
}

func TestRunRunScriptedFailReturns1(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "fail.json")
	raw, _ := json.Marshal(x.Fixture{
		Name: "fail-suite",
		Cases: []x.Case{{
			Name:   "missing-substring",
			Prompt: "say hi",
			Script: []mow.Message{{Role: "assistant", Content: "nope"}},
			Expect: x.Expect{Contains: []string{"must-appear"}},
		}},
	})
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if code := run([]string{"run", path}); code != 1 {
		t.Fatalf("scripted fail: want exit 1, got %d", code)
	}
}

func TestRunRunJSONFlagPrintsReport(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ok.json")
	raw, _ := json.Marshal(x.Fixture{
		Name: "json-suite",
		Cases: []x.Case{{
			Name:   "ok",
			Prompt: "ping",
			Script: []mow.Message{{Role: "assistant", Content: "pong"}},
			Expect: x.Expect{Contains: []string{"pong"}},
		}},
	})
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}

	// Capture stdout for the --json path.
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = w
	code := run([]string{"run", "--json", path})
	w.Close()
	os.Stdout = old

	if code != 0 {
		t.Fatalf("json pass: want exit 0, got %d", code)
	}
	buf := make([]byte, 1<<16)
	n, _ := r.Read(buf)
	r.Close()
	var sr x.SuiteReport
	if err := json.Unmarshal(buf[:n], &sr); err != nil {
		t.Fatalf("stdout not valid suite JSON: %v\n%s", err, string(buf[:n]))
	}
	if !sr.OK || sr.Passed != 1 {
		t.Fatalf("suite report: %+v", sr)
	}
}

// runRun parses flags itself; a bad flag value must surface as exit 2.

func TestRunRunBadFlagReturns2(t *testing.T) {
	// --timeout is a Duration; a malformed value is a parse error (exit 2).
	dir := t.TempDir()
	path := filepath.Join(dir, "ok.json")
	raw, _ := json.Marshal(x.Fixture{
		Cases: []x.Case{{
			Prompt: "p",
			Script: []mow.Message{{Role: "assistant", Content: "x"}},
		}},
	})
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if code := run([]string{"run", "--timeout=not-a-duration", path}); code != 2 {
		t.Fatalf("bad flag: want exit 2, got %d", code)
	}
}

// Regression: a multi-case suite with one pass + one fail still exits 1, and
// the text report names both. This exercises RunFixture's "do not stop on
// first failure" contract through the CLI path.

func TestRunRunMixedSuiteExits1(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mixed.json")
	raw, _ := json.Marshal(x.Fixture{
		Name: "mixed",
		Cases: []x.Case{
			{Name: "ok", Prompt: "p", Script: []mow.Message{{Role: "assistant", Content: "good"}}, Expect: x.Expect{Contains: []string{"good"}}},
			{Name: "bad", Prompt: "p", Script: []mow.Message{{Role: "assistant", Content: "bad"}}, Expect: x.Expect{Contains: []string{"good"}}},
		},
	})
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = w
	code := run([]string{"run", path})
	w.Close()
	os.Stdout = old

	if code != 1 {
		t.Fatalf("mixed: want exit 1, got %d", code)
	}
	buf := make([]byte, 1<<16)
	n, _ := r.Read(buf)
	r.Close()
	out := string(buf[:n])
	if !strings.Contains(out, "ok") || !strings.Contains(out, "FAIL") {
		t.Fatalf("report missing ok/FAIL markers:\n%s", out)
	}
	if !strings.Contains(out, "passed=1 failed=1") {
		t.Fatalf("report missing totals:\n%s", out)
	}
}

// Ensure the scripted run path does not leak a live-model attempt: Run with a
// scripted case under a real (non-test) context must succeed with no API key
// in the environment.

func TestRunScriptedNoAPIKey(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("MOW_API_KEY", "")
	t.Setenv("ANTHROPIC_API_KEY", "")
	dir := t.TempDir()
	_, err := x.Run(context.Background(), x.Case{
		Prompt: "hi",
		Script: []mow.Message{{Role: "assistant", Content: "hi back"}},
		Expect: x.Expect{Contains: []string{"hi back"}},
	}, x.Options{Workspace: dir})
	if err != nil {
		t.Fatalf("scripted run must not need an API key: %v", err)
	}
}
