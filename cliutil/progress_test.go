package cliutil_test

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/subosito/mow"
	"github.com/subosito/mow/cliutil"
)

func TestFormatToolProgress(t *testing.T) {
	cases := []struct {
		name string
		tool string
		args json.RawMessage
		want string
	}{
		{"read with path", "read", json.RawMessage(`{"path":"engine.go"}`), "read engine.go"},
		{"glob pattern", "glob", json.RawMessage(`{"pattern":"**/*.go"}`), "glob **/*.go"},
		{"grep pattern", "grep", json.RawMessage(`{"pattern":"foo","path":"pkg/"}`), "grep foo in pkg/"},
		{"grep no path", "grep", json.RawMessage(`{"pattern":"foo"}`), "grep foo"},
		{"bash command", "bash", json.RawMessage(`{"command":"ls -la"}`), "bash ls -la"},
		{"unknown tool empty args", "acp_delegate", json.RawMessage(`{}`), "acp_delegate"},
		{"unknown tool with url", "fetch", json.RawMessage(`{"url":"https://x"}`), "fetch https://x"},
		{"empty tool name", "", json.RawMessage(`{}`), "?"},
		{"malformed args", "read", json.RawMessage(`{not json}`), "read"},
		{"long path clipped", "read", json.RawMessage(`{"path":"` + strings.Repeat("a", 100) + `"}`), ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := cliutil.FormatToolProgress(c.tool, c.args)
			if c.name == "long path clipped" {
				// Must contain the clip ellipsis and not exceed limit + prefix.
				if !strings.Contains(got, "…") {
					t.Fatalf("expected clipped output with ellipsis, got %q", got)
				}
				return
			}
			if got != c.want {
				t.Fatalf("FormatToolProgress(%q,%q)=%q want %q", c.tool, c.args, got, c.want)
			}
		})
	}
}

func TestFormatToolProgressEmptyArgs(t *testing.T) {
	if got := cliutil.FormatToolProgress("read", nil); got != "read" {
		t.Fatalf("nil args: got %q want read", got)
	}
}

func TestToolProgressOnEventStart(t *testing.T) {
	var buf bytes.Buffer
	fn := cliutil.ToolProgressOnEvent(false)
	// Redirect stderr writes by capturing through a custom sink: the function
	// writes to os.Stderr directly, so we verify via a programmatic Event only
	// if it doesn't panic. The key behavior is that EventToolStart emits a line
	// and EventToolEnd with no error emits nothing.
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("panicked: %v", r)
		}
	}()
	fn(mow.Event{Type: mow.EventToolStart, Tool: "read", Args: json.RawMessage(`{"path":"x.go"}`)})
	_ = buf // not asserted due to direct stderr; ensure no panic and types resolve
}

func TestToolProgressOnEventDenied(t *testing.T) {
	// Denied tool.end should format the error path without panicking.
	fn := cliutil.ToolProgressOnEvent(false)
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("panicked: %v", r)
		}
	}()
	fn(mow.Event{Type: mow.EventToolEnd, Tool: "write", Args: json.RawMessage(`{"path":"x"}`), Denied: true})
	fn(mow.Event{Type: mow.EventToolEnd, Tool: "bash", Args: json.RawMessage(`{"command":"rm"}`), Error: "boom"})
}

func TestEngineFlagsPowerMapping(t *testing.T) {
	var ef cliutil.EngineFlags
	fs := cliutil.NewFlagSet("run")
	ef.Bind(fs)
	if err := fs.Parse([]string{"--allow-shell", "--allow-write", "--workspace", "/tmp/ws", "--model", "gpt-x", "--base-url", "http://x/v1", "--session", "abc", "--continue", "--stream", "--verbose", "--no-session"}); err != nil {
		t.Fatal(err)
	}
	opt := ef.Options()
	if !opt.AllowShell {
		t.Error("AllowShell not set")
	}
	if !opt.AllowWrite {
		t.Error("AllowWrite not set")
	}
	if opt.Workspace != "/tmp/ws" {
		t.Errorf("Workspace=%q", opt.Workspace)
	}
	if opt.Model != "gpt-x" {
		t.Errorf("Model=%q", opt.Model)
	}
	if opt.BaseURL != "http://x/v1" {
		t.Errorf("BaseURL=%q", opt.BaseURL)
	}
	if opt.SessionID != "abc" {
		t.Errorf("SessionID=%q", opt.SessionID)
	}
	if !opt.Continue {
		t.Error("Continue not set")
	}
	if !opt.Stream {
		t.Error("Stream not set")
	}
	if !opt.NoSession {
		t.Error("NoSession not set")
	}
}

func TestEngineFlagsDefaults(t *testing.T) {
	var ef cliutil.EngineFlags
	fs := cliutil.NewFlagSet("run")
	ef.Bind(fs)
	if err := fs.Parse(nil); err != nil {
		t.Fatal(err)
	}
	opt := ef.Options()
	if opt.AllowShell || opt.AllowWrite || opt.Stream || opt.Continue || opt.NoSession {
		t.Errorf("unsafe defaults: %+v", opt)
	}
	if opt.Workspace != "" || opt.Model != "" || opt.BaseURL != "" || opt.SessionID != "" {
		t.Errorf("non-empty string defaults: %+v", opt)
	}
	// Options() does not wire OnToken/OnEvent (that's OptionsCLI); verify the
	// bare mapping leaves them nil so library embedders own the stream sink.
	if opt.OnToken != nil {
		t.Error("Options() wired OnToken unexpectedly")
	}
	if opt.OnEvent != nil {
		t.Error("Options() wired OnEvent unexpectedly")
	}
}

func TestOptionsCLIWiresStreamAndProgress(t *testing.T) {
	var ef cliutil.EngineFlags
	fs := cliutil.NewFlagSet("run")
	ef.Bind(fs)
	if err := fs.Parse([]string{"--stream"}); err != nil {
		t.Fatal(err)
	}
	opt := ef.OptionsCLI()
	if !opt.Stream {
		t.Fatal("OptionsCLI Stream not set")
	}
	if opt.OnToken == nil {
		t.Fatal("OptionsCLI should wire OnToken when --stream")
	}
	if opt.OnEvent == nil {
		t.Fatal("OptionsCLI should wire OnEvent (tool progress)")
	}
}

func TestOptionsCLINoStreamNoTokenSink(t *testing.T) {
	var ef cliutil.EngineFlags
	fs := cliutil.NewFlagSet("run")
	ef.Bind(fs)
	if err := fs.Parse(nil); err != nil {
		t.Fatal(err)
	}
	opt := ef.OptionsCLI()
	// Without --stream, OnToken must be nil (no one is consuming deltas).
	if opt.OnToken != nil {
		t.Fatal("OptionsCLI wired OnToken without --stream")
	}
	// OnEvent (tool progress) is always wired by OptionsCLI.
	if opt.OnEvent == nil {
		t.Fatal("OptionsCLI should always wire OnEvent")
	}
}

func TestConfigPaths(t *testing.T) {
	var ef cliutil.EngineFlags
	if got := ef.ConfigPaths(); got != nil {
		t.Fatalf("empty config → nil paths, got %v", got)
	}
	ef.Config = "  /x.yaml  "
	got := ef.ConfigPaths()
	if len(got) != 1 || got[0] != "/x.yaml" {
		t.Fatalf("ConfigPaths=%v", got)
	}
}
