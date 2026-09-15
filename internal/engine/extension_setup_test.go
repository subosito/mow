package engine

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/subosito/mow/ext"
)

type setupTestTool string

func (t setupTestTool) Name() string                                          { return string(t) }
func (t setupTestTool) Description() string                                   { return "setup test" }
func (t setupTestTool) Parameters() json.RawMessage                           { return json.RawMessage(`{"type":"object"}`) }
func (t setupTestTool) Exec(context.Context, json.RawMessage) (string, error) { return "ok", nil }

func setupTestOptions(ws string) Options {
	return Options{Workspace: ws, NoSession: true, Chat: func(context.Context, []Message, []ToolSpec) (Message, error) {
		return Message{Role: "assistant", Content: "ok"}, nil
	}}
}

func TestExtensionSetupFailureReleasesLock(t *testing.T) {
	ext.Reset()
	t.Cleanup(ext.Reset)
	want := errors.New("setup failed")
	ext.RegisterBeforeNewContext(func(ext.SetupContext) error { return want })
	if _, err := New(setupTestOptions(t.TempDir())); !errors.Is(err, want) {
		t.Fatalf("got %v, want setup failure", err)
	}
	ext.Reset()
	e, err := New(setupTestOptions(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
}

func TestConcurrentExtensionSetupIsolated(t *testing.T) {
	ext.Reset()
	t.Cleanup(ext.Reset)
	aWS, bWS := t.TempDir(), t.TempDir()
	entered, release := make(chan struct{}), make(chan struct{})
	ext.RegisterBeforeNewContext(func(ctx ext.SetupContext) error {
		if ctx.LoadUserConfig {
			t.Error("unexpected host config")
		}
		name := "setup_b"
		if ctx.Workspace == aWS {
			close(entered)
			<-release
			name = "setup_a"
		}
		ext.RegisterTool(setupTestTool(name))
		return nil
	})
	type result struct {
		e   *Engine
		err error
	}
	aDone, bDone := make(chan result, 1), make(chan result, 1)
	go func() { e, err := New(setupTestOptions(aWS)); aDone <- result{e, err} }()
	<-entered
	go func() { e, err := New(setupTestOptions(bWS)); bDone <- result{e, err} }()
	close(release)
	a, b := <-aDone, <-bDone
	for _, r := range []result{a, b} {
		if r.err != nil {
			t.Fatal(r.err)
		}
		t.Cleanup(func() { _ = r.e.Close() })
	}
	for _, tc := range []struct {
		e          *Engine
		own, other string
	}{{a.e, "setup_a", "setup_b"}, {b.e, "setup_b", "setup_a"}} {
		own := false
		for _, tool := range tc.e.tools {
			if tool.Name() == tc.other {
				t.Errorf("inherited %s", tc.other)
			}
			own = own || tool.Name() == tc.own
		}
		if !own {
			t.Errorf("missing %s", tc.own)
		}
	}
}

func TestSessionStartHasEngineAndCanConstructEngine(t *testing.T) {
	ext.Reset()
	t.Cleanup(ext.Reset)
	ws := t.TempDir()
	opt := setupTestOptions(ws)
	opt.Hooks.OnSessionStart = []SessionStartFunc{func(ctx context.Context, _ SessionStartEvent) (SessionStartDecision, error) {
		eng := EngineFromContext(ctx)
		if eng == nil {
			t.Fatal("missing engine context")
		}
		if err := eng.AddTool(setupTestTool("session_tool")); err != nil {
			t.Fatal(err)
		}
		child, err := New(setupTestOptions(ws))
		if err == nil {
			_ = child.Close()
		}
		return SessionStartDecision{}, err
	}}
	e, err := New(opt)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	if !toolPresent(e.tools, "session_tool") {
		t.Fatal("lifecycle tool missing")
	}
}

func TestEngineFileAccessHonorsPolicy(t *testing.T) {
	ws := t.TempDir()
	ro := filepath.Join(ws, "protected")
	if err := os.Mkdir(ro, 0700); err != nil {
		t.Fatal(err)
	}
	opt := setupTestOptions(ws)
	opt.AllowWrite = true
	opt.ExtraRootsReadOnly = []string{ro}
	e, err := New(opt)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	if _, err = e.WriteFile("protected/output", []byte("no")); err == nil {
		t.Fatal("read-only root accepted write")
	}
	if _, err = e.WriteFile("output", []byte("yes")); err != nil {
		t.Fatal(err)
	}
	_, data, err := e.ReadFile("output")
	if err != nil || string(data) != "yes" {
		t.Fatalf("read %q: %v", data, err)
	}
	if _, _, err = e.ReadFile(filepath.Join(t.TempDir(), "outside")); err == nil {
		t.Fatal("outside read accepted")
	}
	opt.AllowWrite = false
	e2, err := New(opt)
	if err != nil {
		t.Fatal(err)
	}
	defer e2.Close()
	if _, err = e2.WriteFile("output", []byte("no")); err == nil {
		t.Fatal("write capability not enforced")
	}
}
