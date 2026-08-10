package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/subosito/mow/internal/config"
)

// max_turns is unlimited unless the operator asks for a ceiling.
//
// It used to default to 120, which ended healthy long-running work with
// nothing wrong: a turn count is a poor proxy for cost or progress, and
// packs/goal read the resulting ErrMaxTurns as a *user-set* budget hit and
// failed the goal after five of them. Cost is bounded by max_run_tokens /
// max_run_usd, spinning by ErrStuck; neither needs a turn cap.
func TestMaxTurnsDefaultsUnlimited(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(config.EnvHome, dir)
	f, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if f.Policy.MaxTurns != 0 {
		t.Errorf("max_turns default = %d, want 0 (unlimited)", f.Policy.MaxTurns)
	}
}

// An explicit ceiling is still honoured, and -1 still means unlimited for
// configs written when 120 was the default.
func TestMaxTurnsExplicitStillWorks(t *testing.T) {
	for _, tc := range []struct {
		yaml string
		want int
	}{
		{"policy:\n  max_turns: 200\n", 200},
		{"policy:\n  max_turns: -1\n", 0},
		{"policy:\n  max_turns: 0\n", 0},
	} {
		dir := t.TempDir()
		t.Setenv(config.EnvHome, dir)
		if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(tc.yaml), 0o644); err != nil {
			t.Fatal(err)
		}
		f, err := config.Load()
		if err != nil {
			t.Fatal(err)
		}
		if f.Policy.MaxTurns != tc.want {
			t.Errorf("%q -> max_turns = %d, want %d", tc.yaml, f.Policy.MaxTurns, tc.want)
		}
	}
}
