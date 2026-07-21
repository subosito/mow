package contextload

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadSkillsFolderForm(t *testing.T) {
	dir := t.TempDir()

	// Standard skill folder: <name>/SKILL.md.
	hz := filepath.Join(dir, "humanizer")
	if err := os.MkdirAll(hz, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(hz, "SKILL.md"), []byte("humanizer body"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A folder's non-SKILL files are not instructions.
	if err := os.WriteFile(filepath.Join(hz, "README.md"), []byte("readme noise"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A loose top-level .md is no longer a skill (folder-only).
	if err := os.WriteFile(filepath.Join(dir, "loose.md"), []byte("loose body"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A folder without SKILL.md contributes nothing.
	noskill := filepath.Join(dir, "noskill")
	if err := os.MkdirAll(noskill, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(noskill, "notes.md"), []byte("just notes"), 0o644); err != nil {
		t.Fatal(err)
	}

	out := LoadSkills([]string{dir})
	if !strings.Contains(out, "## skill: humanizer") || !strings.Contains(out, "humanizer body") {
		t.Errorf("folder skill missing or mislabeled: %q", out)
	}
	if strings.Contains(out, "loose body") {
		t.Errorf("loose top-level .md must not load (folder-only): %q", out)
	}
	if strings.Contains(out, "readme noise") {
		t.Errorf("folder README must not load as a skill: %q", out)
	}
	if strings.Contains(out, "just notes") {
		t.Errorf("folder without SKILL.md must not load: %q", out)
	}
}

func TestLoadSkillsCaseInsensitiveSkillFile(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "mySkill")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "Skill.md"), []byte("case body"), 0o644); err != nil {
		t.Fatal(err)
	}
	out := LoadSkills([]string{dir})
	if !strings.Contains(out, "case body") {
		t.Errorf("case-insensitive SKILL.md not loaded: %q", out)
	}
}
