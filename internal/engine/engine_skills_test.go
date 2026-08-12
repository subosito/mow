package engine_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/subosito/mow"
)

// makeSkillDir creates a skill dir with <name>/SKILL.md and returns its path.
func makeSkillDir(t *testing.T, skills map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range skills {
		folder := filepath.Join(dir, name)
		if err := os.MkdirAll(folder, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(folder, "SKILL.md"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// TestExplicitSkillsLoadAtStartup verifies that skills named in
// Options.ExplicitSkills load into the system prompt at Engine.New even when
// the selector is on (default) and the first prompt does not mention them.
func TestExplicitSkillsLoadAtStartup(t *testing.T) {
	skillDir := makeSkillDir(t, map[string]string{
		"docker": "DOCKER_SKILL_BODY",
		"go":     "GO_SKILL_BODY",
		"unused": "UNUSED_SKILL_BODY",
	})

	t.Setenv("MOW_HOME", t.TempDir())
	var sawSys string
	eng, err := mow.New(mow.Options{
		NoSession:      true,
		ExplicitSkills: []string{"docker", "go"},
		SkillsDirs:     []string{skillDir},
		Chat: func(ctx context.Context, messages []mow.Message, tools []mow.ToolSpec) (mow.Message, error) {
			for _, m := range messages {
				if m.Role == "system" {
					sawSys = m.Content
				}
			}
			return mow.Message{Role: "assistant", Content: "ok"}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := eng.Prompt(context.Background(), "unrelated prompt that mentions no skills"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sawSys, "DOCKER_SKILL_BODY") {
		t.Errorf("explicit skill 'docker' not in system prompt: %q", sawSys[:min(200, len(sawSys))])
	}
	if !strings.Contains(sawSys, "GO_SKILL_BODY") {
		t.Errorf("explicit skill 'go' not in system prompt: %q", sawSys[:min(200, len(sawSys))])
	}
	if strings.Contains(sawSys, "UNUSED_SKILL_BODY") {
		t.Errorf("non-explicit skill leaked into prompt: %q", sawSys)
	}
}

// TestExplicitSkillsMergeWithPromptSelected verifies that explicit skills and
// prompt-matched skills both appear in the system prompt (the first-prompt
// selector still runs alongside explicit).
func TestExplicitSkillsMergeWithPromptSelected(t *testing.T) {
	skillDir := makeSkillDir(t, map[string]string{
		"docker":  "DOCKER_SKILL_BODY",
		"review":  "REVIEW_SKILL_BODY",
		"private": "PRIVATE_SKILL_BODY",
	})

	t.Setenv("MOW_HOME", t.TempDir())
	var sawSys string
	eng, err := mow.New(mow.Options{
		NoSession:      true,
		ExplicitSkills: []string{"docker"},
		SkillsDirs:     []string{skillDir},
		Chat: func(ctx context.Context, messages []mow.Message, tools []mow.ToolSpec) (mow.Message, error) {
			for _, m := range messages {
				if m.Role == "system" {
					sawSys = m.Content
				}
			}
			return mow.Message{Role: "assistant", Content: "ok"}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	// "review" matches the prompt; "docker" is explicit; "private" matches neither.
	if _, err := eng.Prompt(context.Background(), "please review this"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sawSys, "DOCKER_SKILL_BODY") {
		t.Errorf("explicit skill 'docker' missing: %q", sawSys[:min(200, len(sawSys))])
	}
	if !strings.Contains(sawSys, "REVIEW_SKILL_BODY") {
		t.Errorf("prompt-matched skill 'review' missing: %q", sawSys[:min(200, len(sawSys))])
	}
	if strings.Contains(sawSys, "PRIVATE_SKILL_BODY") {
		t.Errorf("non-matched non-explicit skill leaked: %q", sawSys)
	}
}

// TestExplicitSkillsUnknownNameSilent verifies an unknown explicit skill name
// does not error and produces an empty skill body.
func TestExplicitSkillsUnknownNameSilent(t *testing.T) {
	skillDir := makeSkillDir(t, map[string]string{"docker": "DOCKER_SKILL_BODY"})

	t.Setenv("MOW_HOME", t.TempDir())
	eng, err := mow.New(mow.Options{
		NoSession:      true,
		ExplicitSkills: []string{"does-not-exist"},
		SkillsDirs:     []string{skillDir},
		Chat: func(ctx context.Context, messages []mow.Message, tools []mow.ToolSpec) (mow.Message, error) {
			return mow.Message{Role: "assistant", Content: "ok"}, nil
		},
	})
	if err != nil {
		t.Fatalf("unknown explicit skill should not error: %v", err)
	}
	_ = eng
}

// TestExplicitSkillsDedupWithPromptMatch verifies that if a skill is both
// explicit and prompt-matched, it appears only once in the system prompt.
func TestExplicitSkillsDedupWithPromptMatch(t *testing.T) {
	skillDir := makeSkillDir(t, map[string]string{"docker": "DOCKER_SKILL_BODY"})

	t.Setenv("MOW_HOME", t.TempDir())
	var sawSys string
	eng, err := mow.New(mow.Options{
		NoSession:      true,
		ExplicitSkills: []string{"docker"},
		SkillsDirs:     []string{skillDir},
		Chat: func(ctx context.Context, messages []mow.Message, tools []mow.ToolSpec) (mow.Message, error) {
			for _, m := range messages {
				if m.Role == "system" {
					sawSys = m.Content
				}
			}
			return mow.Message{Role: "assistant", Content: "ok"}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := eng.Prompt(context.Background(), "docker help"); err != nil {
		t.Fatal(err)
	}
	if c := strings.Count(sawSys, "## skill: docker"); c != 1 {
		t.Errorf("duped skill 'docker' appears %d times, want 1", c)
	}
}

// TestExplicitSkillsSelectorOffNoDouble verifies that when selector is off
// (all skills loaded), explicit skills don't duplicate.
func TestExplicitSkillsSelectorOffNoDouble(t *testing.T) {
	skillDir := makeSkillDir(t, map[string]string{"docker": "DOCKER_SKILL_BODY"})

	home := t.TempDir()
	t.Setenv("MOW_HOME", home)
	// Project config disables the selector so all skills load at startup.
	ws := t.TempDir()
	wsCfgDir := filepath.Join(ws, ".mow")
	if err := os.MkdirAll(wsCfgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfgBody := "skills:\n  selector: false\n"
	if err := os.WriteFile(filepath.Join(wsCfgDir, "config.yaml"), []byte(cfgBody), 0o600); err != nil {
		t.Fatal(err)
	}
	// Grant trust per-invocation so the project config loads without a marker.
	t.Setenv("MOW_TRUST_PROJECT", "1")

	var sawSys string
	eng, err := mow.New(mow.Options{
		LoadUserConfig: true, // project trust / .mow/config is host state
		NoSession:      true,
		Workspace:      ws,
		ExplicitSkills: []string{"docker"},
		SkillsDirs:     []string{skillDir},
		Chat: func(ctx context.Context, messages []mow.Message, tools []mow.ToolSpec) (mow.Message, error) {
			for _, m := range messages {
				if m.Role == "system" {
					sawSys = m.Content
				}
			}
			return mow.Message{Role: "assistant", Content: "ok"}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := eng.Prompt(context.Background(), "unrelated"); err != nil {
		t.Fatal(err)
	}
	if c := strings.Count(sawSys, "## skill: docker"); c != 1 {
		t.Errorf("skill 'docker' appears %d times when selector off + explicit, want 1", c)
	}
}
