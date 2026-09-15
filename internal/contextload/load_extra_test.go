package contextload

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestLoadHierarchy(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	subDir := filepath.Join(tmpDir, "child", "subchild")
	if err := os.MkdirAll(subDir, 0755); err != nil {
		t.Fatal(err)
	}

	// The walk stops at the enclosing git root, so mark tmpDir as one.
	if err := os.MkdirAll(filepath.Join(tmpDir, ".git"), 0755); err != nil {
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

func TestReadFileCappedTruncatesLargeFiles(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cases := []struct {
		name  string
		body  []byte
		trunc bool
	}{
		{"small", []byte("# rules\n"), false},
		{"exactly cap", bytes.Repeat([]byte("a"), maxContextFileBytes), false},
		{"over cap", bytes.Repeat([]byte("a"), maxContextFileBytes+10), true},
		// A multibyte rune straddling the cap must not leave a torn byte.
		{"multibyte at cap", append(bytes.Repeat([]byte("a"), maxContextFileBytes-1), []byte("é…")...), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(dir, strings.ReplaceAll(tc.name, " ", "_"))
			if err := os.WriteFile(path, tc.body, 0o600); err != nil {
				t.Fatal(err)
			}
			got, err := readFileCapped(path)
			if err != nil {
				t.Fatal(err)
			}
			if !utf8.Valid(got) {
				t.Fatal("result is not valid UTF-8")
			}
			if has := bytes.HasSuffix(got, []byte(truncatedMarker)); has != tc.trunc {
				t.Fatalf("truncated=%v want %v (len=%d)", has, tc.trunc, len(got))
			}
			if !tc.trunc && !bytes.Equal(got, tc.body) {
				t.Fatal("small file altered")
			}
			if tc.trunc && len(got) > maxContextFileBytes+len(truncatedMarker) {
				t.Fatalf("len=%d exceeds cap", len(got))
			}
		})
	}
	// The AGENTS.md loader and the skill collector both go through the cap.
	ws := filepath.Join(dir, "ws")
	if err := os.MkdirAll(filepath.Join(ws, ".mow", "skills", "big"), 0o755); err != nil {
		t.Fatal(err)
	}
	big := bytes.Repeat([]byte("x"), maxContextFileBytes+1)
	if err := os.WriteFile(filepath.Join(ws, "AGENTS.md"), big, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, ".mow", "skills", "big", "SKILL.md"), append([]byte("---\nname: big\n---\n"), big...), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := readTrimmed(filepath.Join(ws, "AGENTS.md")); !strings.HasSuffix(got, strings.TrimSpace(truncatedMarker)) {
		t.Fatal("AGENTS.md not truncated")
	}
	if got := LoadSkills([]string{filepath.Join(ws, ".mow", "skills")}); !strings.Contains(got, "[truncated") {
		t.Fatal("SKILL.md not truncated")
	}
}

func TestLoadStopsAtGitRoot(t *testing.T) {
	t.Parallel()

	outside := t.TempDir()
	repo := filepath.Join(outside, "repo")
	sub := filepath.Join(repo, "pkg")
	if err := os.MkdirAll(filepath.Join(sub, "x"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0755); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(outside, "AGENTS.md"), []byte("# Outside instructions\n"), 0600)
	_ = os.WriteFile(filepath.Join(repo, "AGENTS.md"), []byte("# Repo instructions\n"), 0600)
	_ = os.WriteFile(filepath.Join(sub, "AGENTS.md"), []byte("# Pkg instructions\n"), 0600)

	res, err := Load(sub)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if strings.Contains(res, "Outside instructions") {
		t.Errorf("walked above the git root:\n%s", res)
	}
	if !strings.Contains(res, "Repo instructions") || !strings.Contains(res, "Pkg instructions") {
		t.Errorf("missing repo/workspace instructions:\n%s", res)
	}
}

func TestLoadStopsAtHomeWithoutGitRoot(t *testing.T) {
	base := t.TempDir()
	home := filepath.Join(base, "home")
	if err := os.MkdirAll(home, 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("MOW_HOME", filepath.Join(home, ".mow"))

	ws := filepath.Join(home, "work", "proj")
	if err := os.MkdirAll(ws, 0755); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(base, "AGENTS.md"), []byte("# Above home\n"), 0600)
	_ = os.WriteFile(filepath.Join(home, "AGENTS.md"), []byte("# Home instructions\n"), 0600)
	_ = os.WriteFile(filepath.Join(ws, "AGENTS.md"), []byte("# Proj instructions\n"), 0600)

	res, err := Load(ws)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if strings.Contains(res, "Above home") {
		t.Errorf("walked above $HOME:\n%s", res)
	}
	if !strings.Contains(res, "Home instructions") || !strings.Contains(res, "Proj instructions") {
		t.Errorf("missing home/workspace instructions:\n%s", res)
	}
}

func TestLoadHermeticStopsAtWorkspace(t *testing.T) {
	t.Parallel()

	repo := t.TempDir()
	ws := filepath.Join(repo, "sub")
	if err := os.MkdirAll(ws, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0755); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(repo, "AGENTS.md"), []byte("# Parent instructions\n"), 0600)
	_ = os.WriteFile(filepath.Join(ws, "AGENTS.md"), []byte("# Workspace instructions\n"), 0600)

	res, err := LoadHermetic(ws)
	if err != nil {
		t.Fatalf("LoadHermetic: %v", err)
	}
	if strings.Contains(res, "Parent instructions") {
		t.Errorf("hermetic load walked to the parent:\n%s", res)
	}
	if !strings.Contains(res, "Workspace instructions") {
		t.Errorf("missing workspace instructions:\n%s", res)
	}
}
