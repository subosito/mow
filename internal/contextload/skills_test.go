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

func TestLoadSelectedSkillsByPrompt(t *testing.T) {
	dir := t.TempDir()
	for name, body := range map[string]string{"review": "review rules", "deploy": "deploy rules"} {
		folder := filepath.Join(dir, name)
		if err := os.MkdirAll(folder, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(folder, "SKILL.md"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	got := LoadSelectedSkills([]string{dir}, "Please REVIEW this patch", true)
	if !strings.Contains(got, "review rules") || strings.Contains(got, "deploy rules") {
		t.Fatalf("selected=%q", got)
	}
	if got := LoadSelectedSkills([]string{dir}, "unrelated task", true); got != "" {
		t.Fatalf("unrelated=%q", got)
	}
	all := LoadSelectedSkills([]string{dir}, "unrelated task", false)
	if !strings.Contains(all, "review rules") || !strings.Contains(all, "deploy rules") {
		t.Fatalf("disabled=%q", all)
	}
}

func TestLoadExplicitSkills(t *testing.T) {
	dir := t.TempDir()
	for name, body := range map[string]string{"docker": "docker body", "go": "go body", "nodeploy": "nodeploy body"} {
		folder := filepath.Join(dir, name)
		if err := os.MkdirAll(folder, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(folder, "SKILL.md"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// Load explicit named skills — case-insensitive, regardless of prompt.
	got := LoadExplicitSkills([]string{dir}, []string{"Docker", "go"})
	if !strings.Contains(got, "docker body") || !strings.Contains(got, "go body") {
		t.Errorf("explicit skills not loaded: %q", got)
	}
	if strings.Contains(got, "nodeploy body") {
		t.Errorf("non-explicit skill leaked in: %q", got)
	}
	// Unknown names silently ignored, no error.
	got = LoadExplicitSkills([]string{dir}, []string{"nonexistent"})
	if got != "" {
		t.Errorf("unknown name should produce empty: %q", got)
	}
	// Empty names list → empty result.
	got = LoadExplicitSkills([]string{dir}, nil)
	if got != "" {
		t.Errorf("nil names should produce empty: %q", got)
	}
	// Dedup: same name in two dirs loads once.
	dir2 := t.TempDir()
	for name, body := range map[string]string{"docker": "docker body alt"} {
		folder := filepath.Join(dir2, name)
		if err := os.MkdirAll(folder, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(folder, "SKILL.md"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	got = LoadExplicitSkills([]string{dir, dir2}, []string{"docker"})
	if c := strings.Count(got, "## skill: docker"); c != 1 {
		t.Errorf("duped skill loaded %d times, want 1: %q", c, got)
	}
}

func TestAvailableSkillNames(t *testing.T) {
	dir := t.TempDir()
	for name, body := range map[string]string{"zeta": "z", "alpha": "a", "beta": "b"} {
		folder := filepath.Join(dir, name)
		if err := os.MkdirAll(folder, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(folder, "SKILL.md"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// Folder without SKILL.md is not listed.
	noskill := filepath.Join(dir, "noskill")
	if err := os.MkdirAll(noskill, 0o755); err != nil {
		t.Fatal(err)
	}
	// Hidden folder is not listed.
	hidden := filepath.Join(dir, ".hidden")
	if err := os.MkdirAll(hidden, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(hidden, "SKILL.md"), []byte("hidden"), 0o600); err != nil {
		t.Fatal(err)
	}

	got := AvailableSkillNames([]string{dir})
	want := []string{"alpha", "beta", "zeta"}
	if len(got) != len(want) {
		t.Fatalf("names=%v want %v", got, want)
	}
	for i, n := range want {
		if got[i] != n {
			t.Errorf("names[%d]=%q want %q", i, got[i], n)
		}
	}

	// Missing dir is silently skipped.
	got = AvailableSkillNames([]string{filepath.Join(dir, "does", "not", "exist")})
	if len(got) != 0 {
		t.Errorf("missing dir should yield no names: %v", got)
	}

	// Dedup across dirs: same name in two dirs appears once.
	dir2 := t.TempDir()
	folder := filepath.Join(dir2, "alpha")
	if err := os.MkdirAll(folder, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(folder, "SKILL.md"), []byte("dup"), 0o600); err != nil {
		t.Fatal(err)
	}
	got = AvailableSkillNames([]string{dir, dir2})
	if len(got) != 3 {
		t.Errorf("duped name not deduped: %v", got)
	}
}
