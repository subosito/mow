package contextload

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseSkillMarkdownFrontmatter(t *testing.T) {
	raw := "---\nname: code-review\ndescription: Review a PR.\nlicense: Apache-2.0\n---\n\nUse the checklist.\n"
	got := parseSkillMarkdown("review", raw)
	if got.Name != "code-review" {
		t.Fatalf("name=%q", got.Name)
	}
	if got.Folder != "review" {
		t.Fatalf("folder=%q", got.Folder)
	}
	if got.Description != "Review a PR." {
		t.Fatalf("description=%q", got.Description)
	}
	if got.Body != "Use the checklist." {
		t.Fatalf("body=%q", got.Body)
	}
	if strings.Contains(got.Body, "---") || strings.Contains(got.Body, "license:") {
		t.Fatalf("frontmatter leaked into body: %q", got.Body)
	}
}

func TestParseSkillMarkdownInvalidNameKeepsFolder(t *testing.T) {
	raw := "---\nname: Not Valid\ndescription: x\n---\nbody\n"
	got := parseSkillMarkdown("review", raw)
	if got.Name != "review" {
		t.Fatalf("invalid spec name must fall back to folder, got %q", got.Name)
	}
	if got.Body != "body" {
		t.Fatalf("body=%q", got.Body)
	}
}

func TestLoadSkillsStripsFrontmatter(t *testing.T) {
	dir := t.TempDir()
	folder := filepath.Join(dir, "review")
	if err := os.MkdirAll(folder, 0o755); err != nil {
		t.Fatal(err)
	}
	raw := "---\nname: code-review\ndescription: Review a PR.\n---\nUse the checklist.\n"
	if err := os.WriteFile(filepath.Join(folder, "SKILL.md"), []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	out := LoadSkills([]string{dir})
	if !strings.Contains(out, "## skill: code-review") {
		t.Fatalf("label missing: %q", out)
	}
	if !strings.Contains(out, "Use the checklist.") {
		t.Fatalf("body missing: %q", out)
	}
	if strings.Contains(out, "description:") || strings.Contains(out, "---") {
		t.Fatalf("frontmatter leaked: %q", out)
	}
}

func TestDisableModelInvocationSkipsSelector(t *testing.T) {
	dir := t.TempDir()
	folder := filepath.Join(dir, "secrets")
	if err := os.MkdirAll(folder, 0o755); err != nil {
		t.Fatal(err)
	}
	raw := "---\nname: secrets\ndescription: Manual only.\ndisable-model-invocation: true\n---\nNever leak keys.\n"
	if err := os.WriteFile(filepath.Join(folder, "SKILL.md"), []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := LoadSelectedSkills([]string{dir}, "please use secrets", true); got != "" {
		t.Fatalf("selector must skip disable-model-invocation: %q", got)
	}
	if got := LoadExplicitSkills([]string{dir}, []string{"secrets"}); !strings.Contains(got, "Never leak keys.") {
		t.Fatalf("explicit load must still work: %q", got)
	}
}

func TestAvailableSkillInfosExposesDescription(t *testing.T) {
	dir := t.TempDir()
	folder := filepath.Join(dir, "docs")
	if err := os.MkdirAll(folder, 0o755); err != nil {
		t.Fatal(err)
	}
	raw := "---\nname: docs-writer\ndescription: Write the README.\n---\nBe concise.\n"
	if err := os.WriteFile(filepath.Join(folder, "SKILL.md"), []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	infos := AvailableSkillInfos([]string{dir})
	if len(infos) != 1 {
		t.Fatalf("infos=%v", infos)
	}
	if infos[0].Folder != "docs" || infos[0].Name != "docs-writer" {
		t.Fatalf("ids: %+v", infos[0])
	}
	if infos[0].Description != "Write the README." {
		t.Fatalf("description=%q", infos[0].Description)
	}
	if infos[0].Body != "" {
		t.Fatalf("listing must omit body: %q", infos[0].Body)
	}
}
