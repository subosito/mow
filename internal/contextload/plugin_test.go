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

func TestListPluginsReadsClaudePluginMCPAndHooks(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "context-mode")
	if err := os.MkdirAll(filepath.Join(dir, ".claude-plugin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "hooks"), 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := `{
		"name": "context-mode",
		"mcpServers": {
			"context-mode": {
				"command": "node",
				"args": ["${CLAUDE_PLUGIN_ROOT}/start.mjs"]
			}
		}
	}`
	if err := os.WriteFile(filepath.Join(dir, ".claude-plugin", "plugin.json"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "hooks", "hooks.json"), []byte(`{"hooks":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	got := ListPlugins([]string{root})
	if len(got) != 1 {
		t.Fatalf("plugins=%v", got)
	}
	p := got[0]
	if p.ID != "context-mode" || p.HooksFile != filepath.Join("hooks", "hooks.json") {
		t.Fatalf("meta: %+v", p)
	}
	if len(p.MCPServers) != 1 || p.MCPServers[0].Name != "context-mode" {
		t.Fatalf("mcp=%+v", p.MCPServers)
	}
	want := filepath.Join(dir, "start.mjs")
	if p.MCPServers[0].Command != "node" || len(p.MCPServers[0].Args) != 1 || p.MCPServers[0].Args[0] != want {
		t.Fatalf("command=%q args=%v", p.MCPServers[0].Command, p.MCPServers[0].Args)
	}
}

func TestHostOwnedPluginRootsSkipsProjectDotMow(t *testing.T) {
	home := t.TempDir()
	profile := filepath.Join(home, "workspaces", "mow", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(profile), 0o755); err != nil {
		t.Fatal(err)
	}
	project := filepath.Join(t.TempDir(), ".mow", "config.yaml")
	got := HostOwnedPluginRoots(home, []string{profile, project})
	want := []string{
		filepath.Join(home, "plugins"),
		filepath.Join(home, "workspaces", "mow", "plugins"),
	}
	if !slices.Equal(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestHostOwnedPluginRootsIncludesProfileWithoutConfigFile(t *testing.T) {
	home := t.TempDir()
	overlay := filepath.Join(home, "workspaces", "mow", "config.yaml")
	got := HostOwnedPluginRoots(home, []string{overlay})
	want := []string{
		filepath.Join(home, "plugins"),
		filepath.Join(home, "workspaces", "mow", "plugins"),
	}
	if !slices.Equal(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}
