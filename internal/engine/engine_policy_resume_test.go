package engine_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/subosito/mow"
)

func TestContinueRestoresReadOnlyPolicy(t *testing.T) {
	home := t.TempDir()
	ws := t.TempDir()
	t.Setenv("MOW_HOME", home)
	t.Setenv("MOW_API_KEY", "test-key")
	cfgPath := filepath.Join(home, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte("tools:\n  write: true\n  shell: true\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	first, err := mow.New(mow.Options{
		LoadUserConfig: true,
		Workspace:      ws,
		ConfigPaths:    []string{cfgPath},
		DisableWrite:   true,
		DisableShell:   true,
		DeferLLM:       true,
		Chat: func(context.Context, []mow.Message, []mow.ToolSpec) (mow.Message, error) {
			return mow.Message{Role: "assistant", Content: "ok"}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	sid := first.SessionID()
	if sid == "" {
		t.Fatal("expected session id")
	}
	if first.AllowWrite() || first.AllowShell() {
		t.Fatalf("seed session should be read-only: write=%v shell=%v", first.AllowWrite(), first.AllowShell())
	}
	if _, err := first.Prompt(t.Context(), "saved conversation"); err != nil {
		t.Fatal(err)
	}
	first.Close()

	cont, err := mow.New(mow.Options{
		LoadUserConfig: true,
		Workspace:      ws,
		ConfigPaths:    []string{cfgPath},
		Continue:       true,
		DeferLLM:       true,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cont.Close() })
	if cont.SessionID() != sid {
		t.Fatalf("session=%q want %q", cont.SessionID(), sid)
	}
	if cont.AllowWrite() || cont.AllowShell() {
		t.Fatalf("continue should keep read-only: write=%v shell=%v", cont.AllowWrite(), cont.AllowShell())
	}

	over, err := mow.New(mow.Options{
		LoadUserConfig:       true,
		Workspace:            ws,
		ConfigPaths:          []string{cfgPath},
		Continue:             true,
		AllowWrite:           true,
		AllowShell:           true,
		ExplicitCapabilities: true,
		DeferLLM:             true,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = over.Close() })
	if !over.AllowWrite() || !over.AllowShell() {
		t.Fatalf("explicit flags should beat session: write=%v shell=%v", over.AllowWrite(), over.AllowShell())
	}
}
