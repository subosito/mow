package contextload

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func writePlugin(t *testing.T, root, id, pluginJSON, skillName, skillBody string) {
	t.Helper()
	dir := filepath.Join(root, id)
	if err := os.MkdirAll(filepath.Join(dir, "skills", skillName), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "plugin.json"), []byte(pluginJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "skills", skillName, "SKILL.md"), []byte(skillBody), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestListPluginsReadsManifestAndSkills(t *testing.T) {
	root := t.TempDir()
	writePlugin(t, root, "review-kit", `{
		"name": "Review Kit",
		"version": "1.2.0",
		"description": "PR review helpers",
		"default-skills": ["review"]
	}`, "review", "---\nname: code-review\ndescription: Review a PR.\n---\nUse the checklist.\n")
	got := ListPlugins([]string{root})
	if len(got) != 1 {
		t.Fatalf("plugins=%v", got)
	}
	p := got[0]
	if p.ID != "review-kit" || p.Name != "Review Kit" || p.Version != "1.2.0" {
		t.Fatalf("meta: %+v", p)
	}
	if p.Description != "PR review helpers" {
		t.Fatalf("description=%q", p.Description)
	}
	if !slices.Equal(p.SkillFolders, []string{"review"}) {
		t.Fatalf("skills=%v", p.SkillFolders)
	}
	if !slices.Equal(p.DefaultSkills, []string{"review"}) {
		t.Fatalf("defaults=%v", p.DefaultSkills)
	}
	out := LoadSkills(PluginSkillDirs([]string{root}))
	if !strings.Contains(out, "## skill: code-review") || !strings.Contains(out, "Use the checklist.") {
		t.Fatalf("plugin skills not loaded: %q", out)
	}
	if strings.Contains(out, "description:") {
		t.Fatalf("frontmatter leaked: %q", out)
	}
}

func TestPluginAlwaysSkillsBecomeDefaults(t *testing.T) {
	root := t.TempDir()
	writePlugin(t, root, "ops", `{"name":"ops","always":true}`, "pager", "Page on-call.\n")
	names := PluginDefaultSkillNames([]string{root})
	if !slices.Equal(names, []string{"pager"}) {
		t.Fatalf("defaults=%v", names)
	}
}

func TestListPluginsSkipsFolderWithoutManifest(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "not-a-plugin", "skills"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := ListPlugins([]string{root}); len(got) != 0 {
		t.Fatalf("got %v", got)
	}
}
