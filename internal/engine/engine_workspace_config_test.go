package engine

import (
	"os"
	"path/filepath"
	"testing"
)

// --workspace must load THAT directory's trusted project config, not the
// process cwd's: trust and the .mow/config.yaml layer follow the workspace
// the engine runs in.
func TestWorkspaceOptionLoadsItsOwnProjectConfig(t *testing.T) {
	t.Setenv("MOW_HOME", t.TempDir())
	t.Setenv("MOW_TRUST_PROJECT", "1")
	cwdRepo := t.TempDir()
	other := t.TempDir()
	for dir, turns := range map[string]string{cwdRepo: "11", other: "22"} {
		if err := os.MkdirAll(filepath.Join(dir, ".mow"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, ".mow", "config.yaml"), []byte("policy:\n  max_turns: "+turns+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Chdir(cwdRepo)
	eng, err := New(Options{LoadUserConfig: true, Workspace: other, Model: "model-a", NoSession: true, Chat: profileChat()})
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()
	if eng.cfg.Workspace != other {
		t.Fatalf("workspace=%q want %q", eng.cfg.Workspace, other)
	}
	if eng.cfg.Policy.MaxTurns != 22 {
		t.Fatalf("max_turns=%d: want --workspace dir's project config (22), not cwd's (11)", eng.cfg.Policy.MaxTurns)
	}
}
