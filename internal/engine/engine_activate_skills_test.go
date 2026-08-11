package engine_test

import (
	"context"
	"strings"
	"testing"

	"github.com/subosito/mow"
)

// TestAvailableSkillsListsConfiguredDir verifies AvailableSkills returns the
// folder names with SKILL.md from the engine's configured skill dirs.
func TestAvailableSkillsListsConfiguredDir(t *testing.T) {
	skillDir := makeSkillDir(t, map[string]string{
		"zeta":  "z",
		"alpha": "a",
	})

	t.Setenv("MOW_HOME", t.TempDir())
	eng, err := mow.New(mow.Options{
		NoSession:  true,
		SkillsDirs: []string{skillDir},
		Chat: func(ctx context.Context, messages []mow.Message, tools []mow.ToolSpec) (mow.Message, error) {
			return mow.Message{Role: "assistant", Content: "ok"}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	got := eng.AvailableSkills()
	want := []string{"alpha", "zeta"}
	if len(got) != len(want) {
		t.Fatalf("AvailableSkills=%v want %v", got, want)
	}
	for i, n := range want {
		if got[i] != n {
			t.Errorf("AvailableSkills[%d]=%q want %q", i, got[i], n)
		}
	}
}

// TestActivateSkillsLoadsForSubsequentTurns verifies that ActivateSkills merges
// named skills into the live system prompt without a restart, and that a
// following Prompt sees them.
func TestActivateSkillsLoadsForSubsequentTurns(t *testing.T) {
	skillDir := makeSkillDir(t, map[string]string{
		"docker": "DOCKER_SKILL_BODY",
		"go":     "GO_SKILL_BODY",
		"unused": "UNUSED_SKILL_BODY",
	})

	t.Setenv("MOW_HOME", t.TempDir())
	var sawSys string
	eng, err := mow.New(mow.Options{
		NoSession:  true,
		SkillsDirs: []string{skillDir},
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
	// Before any prompt: activate two skills mid-session.
	activated, unknown := eng.ActivateSkills("docker", "go", "nope")
	if len(unknown) != 1 || unknown[0] != "nope" {
		t.Errorf("unknown=%v want [nope]", unknown)
	}
	if len(activated) != 2 {
		t.Errorf("activated=%v want 2 names", activated)
	}
	// Subsequent prompt must see the activated skills.
	if _, err := eng.Prompt(context.Background(), "unrelated prompt"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sawSys, "DOCKER_SKILL_BODY") {
		t.Errorf("activated 'docker' not in system prompt: %q", sawSys[:min(200, len(sawSys))])
	}
	if !strings.Contains(sawSys, "GO_SKILL_BODY") {
		t.Errorf("activated 'go' not in system prompt: %q", sawSys[:min(200, len(sawSys))])
	}
	if strings.Contains(sawSys, "UNUSED_SKILL_BODY") {
		t.Errorf("non-activated skill leaked: %q", sawSys)
	}
}

// TestActivateSkillsIdempotent verifies re-activating an already-loaded skill
// does not duplicate it in the system prompt.
func TestActivateSkillsIdempotent(t *testing.T) {
	skillDir := makeSkillDir(t, map[string]string{"docker": "DOCKER_SKILL_BODY"})

	t.Setenv("MOW_HOME", t.TempDir())
	var sawSys string
	eng, err := mow.New(mow.Options{
		NoSession:  true,
		SkillsDirs: []string{skillDir},
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
	// 'docker' was prompt-matched; activating it again should not duplicate.
	eng.ActivateSkills("docker")
	if _, err := eng.Prompt(context.Background(), "more help"); err != nil {
		t.Fatal(err)
	}
	if c := strings.Count(sawSys, "## skill: docker"); c != 1 {
		t.Errorf("docker appears %d times after re-activation, want 1", c)
	}
}

// TestActivateSkillsPreservesExplicit verifies that explicit CLI skills remain
// after a mid-session activation of a different skill.
func TestActivateSkillsPreservesExplicit(t *testing.T) {
	skillDir := makeSkillDir(t, map[string]string{
		"docker": "DOCKER_SKILL_BODY",
		"go":     "GO_SKILL_BODY",
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
	// Run a prompt so explicit skills load, then activate 'go'.
	if _, err := eng.Prompt(context.Background(), "unrelated"); err != nil {
		t.Fatal(err)
	}
	eng.ActivateSkills("go")
	if _, err := eng.Prompt(context.Background(), "next"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sawSys, "DOCKER_SKILL_BODY") {
		t.Errorf("explicit 'docker' dropped after activation: %q", sawSys[:min(200, len(sawSys))])
	}
	if !strings.Contains(sawSys, "GO_SKILL_BODY") {
		t.Errorf("activated 'go' missing: %q", sawSys[:min(200, len(sawSys))])
	}
}

// TestActivateSkillsDoesNotMutateHistory verifies activation does not append to
// the committed transcript (no phantom user/assistant messages).
func TestActivateSkillsDoesNotMutateHistory(t *testing.T) {
	skillDir := makeSkillDir(t, map[string]string{"docker": "DOCKER_SKILL_BODY"})

	t.Setenv("MOW_HOME", t.TempDir())
	eng, err := mow.New(mow.Options{
		NoSession:  true,
		SkillsDirs: []string{skillDir},
		Chat: func(ctx context.Context, messages []mow.Message, tools []mow.ToolSpec) (mow.Message, error) {
			return mow.Message{Role: "assistant", Content: "ok"}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	before := len(eng.Transcript())
	eng.ActivateSkills("docker")
	after := len(eng.Transcript())
	if before != after {
		t.Errorf("activation changed transcript length: before=%d after=%d", before, after)
	}
}

// TestActivateSkillsConcurrentWithPrompt exercises the promptMu / e.mu locking:
// ActivateSkills must not race a concurrent Prompt. Run under -race.
func TestActivateSkillsConcurrentWithPrompt(t *testing.T) {
	skillDir := makeSkillDir(t, map[string]string{
		"docker": "DOCKER_SKILL_BODY",
		"go":     "GO_SKILL_BODY",
		"review": "REVIEW_SKILL_BODY",
	})

	t.Setenv("MOW_HOME", t.TempDir())
	eng, err := mow.New(mow.Options{
		NoSession:  true,
		SkillsDirs: []string{skillDir},
		Chat: func(ctx context.Context, messages []mow.Message, tools []mow.ToolSpec) (mow.Message, error) {
			return mow.Message{Role: "assistant", Content: "ok"}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	done := make(chan struct{})
	// One goroutine repeatedly activates skills.
	go func() {
		defer close(done)
		for i := 0; i < 50; i++ {
			eng.ActivateSkills("docker", "go", "review")
		}
	}()
	// Main goroutine drives prompts concurrently.
	for i := 0; i < 20; i++ {
		_, _ = eng.Prompt(context.Background(), "do a thing")
	}
	<-done
}
