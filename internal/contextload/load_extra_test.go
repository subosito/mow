package contextload

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadHierarchy(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	subDir := filepath.Join(tmpDir, "child", "subchild")
	if err := os.MkdirAll(subDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Write AGENTS.md in root and subchild
	rootAgents := "# Root AGENTS Instructions\n"
	subAgents := "# Subchild AGENTS Instructions\n"
	subClaude := "# Subchild CLAUDE Instructions\n"

	_ = os.WriteFile(filepath.Join(tmpDir, "AGENTS.md"), []byte(rootAgents), 0600)
	_ = os.WriteFile(filepath.Join(subDir, "AGENTS.md"), []byte(subAgents), 0600)
	_ = os.WriteFile(filepath.Join(subDir, "CLAUDE.md"), []byte(subClaude), 0600)

	// Also write empty file to ensure empty files are skipped
	_ = os.WriteFile(filepath.Join(filepath.Dir(subDir), "AGENTS.md"), []byte("   \n"), 0600)

	res, err := Load(subDir)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	// Root instructions should come before deeper instructions
	idxRoot := strings.Index(res, "Root AGENTS")
	idxSubAgents := strings.Index(res, "Subchild AGENTS")
	idxSubClaude := strings.Index(res, "Subchild CLAUDE")

	if idxRoot < 0 || idxSubAgents < 0 || idxSubClaude < 0 {
		t.Fatalf("missing instruction parts in result:\n%s", res)
	}

	if idxRoot >= idxSubAgents {
		t.Errorf("expected root instructions (idx %d) before subchild instructions (idx %d)", idxRoot, idxSubAgents)
	}
}

func TestLoadAgentsStandardThenMow(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("MOW_HOME", filepath.Join(home, ".mow"))
	if err := os.MkdirAll(filepath.Join(home, ".agents"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, ".mow"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".agents", "AGENTS.md"), []byte("STD_BASE"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".mow", "AGENTS.md"), []byte("MOW_GLOBAL"), 0o600); err != nil {
		t.Fatal(err)
	}
	ws := t.TempDir()
	if err := os.MkdirAll(filepath.Join(ws, ".agents"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, ".agents", "AGENTS.md"), []byte("PROJ_BASE"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, "AGENTS.md"), []byte("PROJ_ACTIVE"), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := Load(ws)
	if err != nil {
		t.Fatal(err)
	}
	idx := []int{
		strings.Index(got, "STD_BASE"),
		strings.Index(got, "MOW_GLOBAL"),
		strings.Index(got, "PROJ_BASE"),
		strings.Index(got, "PROJ_ACTIVE"),
	}
	for i, v := range idx {
		if v < 0 {
			t.Fatalf("missing part %d in:\n%s", i, got)
		}
		if i > 0 && idx[i] < idx[i-1] {
			t.Fatalf("order %d before %d in:\n%s", i, i-1, got)
		}
	}

	hermetic, err := LoadHermetic(ws)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(hermetic, "STD_BASE") || strings.Contains(hermetic, "MOW_GLOBAL") {
		t.Fatalf("hermetic leaked home files:\n%s", hermetic)
	}
	if !strings.Contains(hermetic, "PROJ_BASE") || !strings.Contains(hermetic, "PROJ_ACTIVE") {
		t.Fatalf("hermetic missing project files:\n%s", hermetic)
	}
}

func TestLoadEmptyOrMissingWorkspace(t *testing.T) {
	t.Setenv("MOW_HOME", t.TempDir())

	tmpDir := t.TempDir()

	// Empty directory with no files
	res, err := Load(tmpDir)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if res != "" {
		t.Errorf("expected empty string for directory without instruction files, got %q", res)
	}
}

func TestPathJailFactsFormatting(t *testing.T) {
	t.Parallel()

	t.Run("empty workspace and roots", func(t *testing.T) {
		t.Parallel()
		if got := PathJailFacts("", nil); got != "" {
			t.Errorf("got %q, want empty string", got)
		}
	})

	t.Run("workspace only", func(t *testing.T) {
		t.Parallel()
		got := PathJailFacts("/ws", nil)
		if !strings.Contains(got, "Workspace (relative paths resolve here): /ws") || !strings.Contains(got, "No extra roots") {
			t.Errorf("unexpected PathJailFacts output: %q", got)
		}
	})

	t.Run("extra roots and read only extra roots", func(t *testing.T) {
		t.Parallel()
		extra := []string{"/extra1", "/extra2"}
		extraRO := []string{"/extra_ro"}
		got := PathJailFacts("/ws", extra, extraRO)

		if !strings.Contains(got, "/extra1") || !strings.Contains(got, "/extra_ro") {
			t.Errorf("missing extra roots in output: %q", got)
		}
	})
}

func TestWithOptionalIdentity(t *testing.T) {
	t.Parallel()

	t.Run("include false", func(t *testing.T) {
		t.Parallel()
		got := WithOptionalIdentity(false, "my body")
		if got != "my body" {
			t.Errorf("got %q, want 'my body'", got)
		}
	})

	t.Run("include true with empty body", func(t *testing.T) {
		t.Parallel()
		got := WithOptionalIdentity(true, "")
		if got != DefaultHarnessIdentity {
			t.Errorf("got %q, want %q", got, DefaultHarnessIdentity)
		}
	})

	t.Run("include true with body", func(t *testing.T) {
		t.Parallel()
		got := WithOptionalIdentity(true, "custom instructions")
		if !strings.HasPrefix(got, DefaultHarnessIdentity) || !strings.HasSuffix(got, "custom instructions") {
			t.Errorf("unexpected output: %q", got)
		}
	})
}

func TestSkillLoadingEdgeCases(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()

	// Create skill folders:
	// skill-1/SKILL.md (valid)
	// skill-2/skill.MD (case-insensitive test)
	// skill-3/README.md (no SKILL.md -> skipped)
	// skill-empty/SKILL.md (empty content -> skipped)

	skill1Dir := filepath.Join(tmpDir, "skill-1")
	skill2Dir := filepath.Join(tmpDir, "skill-2")
	skill3Dir := filepath.Join(tmpDir, "skill-3")
	skillEmptyDir := filepath.Join(tmpDir, "skill-empty")

	_ = os.MkdirAll(skill1Dir, 0755)
	_ = os.MkdirAll(skill2Dir, 0755)
	_ = os.MkdirAll(skill3Dir, 0755)
	_ = os.MkdirAll(skillEmptyDir, 0755)

	_ = os.WriteFile(filepath.Join(skill1Dir, "SKILL.md"), []byte("Skill 1 content"), 0600)
	_ = os.WriteFile(filepath.Join(skill2Dir, "skill.MD"), []byte("Skill 2 content"), 0600)
	_ = os.WriteFile(filepath.Join(skill3Dir, "README.md"), []byte("Not a skill"), 0600)
	_ = os.WriteFile(filepath.Join(skillEmptyDir, "SKILL.md"), []byte("   \n"), 0600)

	t.Run("LoadSkills", func(t *testing.T) {
		t.Parallel()
		res := LoadSkills([]string{tmpDir, ""})
		if !strings.Contains(res, "skill: skill-1") || !strings.Contains(res, "skill: skill-2") {
			t.Errorf("missing skills in result: %s", res)
		}
		if strings.Contains(res, "skill-3") || strings.Contains(res, "skill-empty") {
			t.Errorf("unexpected skill loaded in result: %s", res)
		}
	})

	t.Run("LoadSelectedSkills prompt filtering", func(t *testing.T) {
		t.Parallel()
		// Prompt mentioning skill-1
		res1 := LoadSelectedSkills([]string{tmpDir}, "Please use skill-1 for this task", true)
		if !strings.Contains(res1, "skill-1") || strings.Contains(res1, "skill-2") {
			t.Errorf("expected only skill-1, got: %s", res1)
		}

		// Prompt matching no skills
		resNone := LoadSelectedSkills([]string{tmpDir}, "unrelated prompt", true)
		if resNone != "" {
			t.Errorf("expected empty string for non-matching prompt, got: %s", resNone)
		}
	})
}

func TestProjectTrustedHelper(t *testing.T) {
	t.Parallel()

	// Non-trusted directory
	if ProjectTrusted(t.TempDir()) {
		t.Error("expected temp dir to not be trusted by default")
	}
}
