package engine_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/subosito/mow"
)

func TestDisableCapabilitiesOverrideConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("MOW_HOME", home)
	cfg := filepath.Join(home, "config.yaml")
	if err := os.WriteFile(cfg, []byte("tools:\n  enable: [read, bash, write, edit]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	eng, err := mow.New(mow.Options{
		ConfigPaths:  []string{cfg},
		NoSession:    true,
		DisableShell: true,
		DisableWrite: true,
		Chat: func(context.Context, []mow.Message, []mow.ToolSpec) (mow.Message, error) {
			return mow.Message{Role: "assistant", Content: "ok"}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()
	if eng.AllowShell() || eng.AllowWrite() {
		t.Fatalf("capabilities not disabled: shell=%v write=%v", eng.AllowShell(), eng.AllowWrite())
	}
}
