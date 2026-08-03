package eval_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/subosito/mow"
	"github.com/subosito/mow/eval"
)

func TestRunScriptedContains(t *testing.T) {
	dir := t.TempDir()
	// Touch a marker file so a real tool path isn't required for this case.
	_ = os.WriteFile(filepath.Join(dir, "hello.go"), []byte("package h\n"), 0o600)

	rep, err := eval.Run(context.Background(), eval.Case{
		Name:   "scripted-ok",
		Prompt: "say hi",
		Script: []mow.Message{
			{Role: "assistant", Content: "hello from fixture"},
		},
		Expect: eval.Expect{
			Contains:   []string{"hello from fixture"},
			StopReason: mow.StopCompleted,
			MaxTurns:   1,
		},
	}, eval.Options{Workspace: dir})
	if err != nil {
		t.Fatalf("run: %v failures=%v", err, rep.Failures)
	}
	if !rep.OK {
		t.Fatalf("report: %+v", rep)
	}
}

func TestRunScriptedMissingSubstringFails(t *testing.T) {
	_, err := eval.Run(context.Background(), eval.Case{
		Prompt: "x",
		Script: []mow.Message{{Role: "assistant", Content: "nope"}},
		Expect: eval.Expect{Contains: []string{"must-have"}},
	}, eval.Options{Workspace: t.TempDir()})
	if err == nil {
		t.Fatal("expected failure")
	}
}

func TestRunScriptedToolExpectation(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "a.go"), []byte("package a\n"), 0o600)

	// Turn 1: model calls glob; turn 2: final answer.
	script := []mow.Message{
		{
			Role: "assistant",
			ToolCalls: []mow.ToolCall{{
				ID:   "c1",
				Type: "function",
				Function: mow.FunctionCall{
					Name:      "glob",
					Arguments: `{"pattern":"*.go"}`,
				},
			}},
		},
		{Role: "assistant", Content: "found a.go"},
	}
	rep, err := eval.Run(context.Background(), eval.Case{
		Name:   "uses-glob",
		Prompt: "find go files",
		Script: script,
		Expect: eval.Expect{
			Tools:    []string{"glob"},
			Contains: []string{"a.go"},
		},
	}, eval.Options{Workspace: dir})
	if err != nil {
		t.Fatalf("run: %v failures=%v tools=%v", err, rep.Failures, rep.Tools)
	}
}

func TestParseFixtureShapes(t *testing.T) {
	f, err := eval.ParseFixture([]byte(`{"name":"s","cases":[{"prompt":"p","script":[{"role":"assistant","content":"x"}]}]}`))
	if err != nil || len(f.Cases) != 1 || f.Name != "s" {
		t.Fatalf("fixture: %+v err=%v", f, err)
	}
	f, err = eval.ParseFixture([]byte(`{"prompt":"only","script":[{"role":"assistant","content":"y"}]}`))
	if err != nil || len(f.Cases) != 1 {
		t.Fatalf("single: %+v err=%v", f, err)
	}
	f, err = eval.ParseFixture([]byte(`[{"prompt":"a"},{"prompt":"b"}]`))
	if err != nil || len(f.Cases) != 2 {
		t.Fatalf("array: %+v err=%v", f, err)
	}
}

func TestLoadFixtureAndRunSuite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "suite.json")
	raw, _ := json.Marshal(eval.Fixture{
		Name: "unit",
		Cases: []eval.Case{{
			Name:   "one",
			Prompt: "ping",
			Script: []mow.Message{{Role: "assistant", Content: "pong"}},
			Expect: eval.Expect{Contains: []string{"pong"}},
		}},
	})
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	fix, err := eval.LoadFixture(path)
	if err != nil {
		t.Fatal(err)
	}
	sr := eval.RunFixture(context.Background(), fix, eval.Options{Workspace: dir})
	if !sr.OK || sr.Passed != 1 || sr.Failed != 0 {
		t.Fatalf("%+v", sr)
	}
}

func TestNotContains(t *testing.T) {
	_, err := eval.Run(context.Background(), eval.Case{
		Prompt: "x",
		Script: []mow.Message{{Role: "assistant", Content: "secret leaked"}},
		Expect: eval.Expect{NotContains: []string{"secret"}},
	}, eval.Options{Workspace: t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), "forbidden") {
		t.Fatalf("err=%v", err)
	}
}
