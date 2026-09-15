package engine_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/subosito/mow"
)

func TestPluginCommandsActivateOnlyRequestedSkills(t *testing.T) {
	home := t.TempDir()
	t.Setenv("MOW_HOME", home)
	root := filepath.Join(home, "plugins", "review-kit")
	files := map[string]string{
		"plugin.json":              `{"name":"review-kit","default-skills":["baseline"]}`,
		"skills/baseline/SKILL.md": "BASELINE",
		"skills/audit/SKILL.md":    "AUDIT-INSTRUCTIONS",
		"skills/unused/SKILL.md":   "UNUSED-INSTRUCTIONS",
		"commands/audit.toml":      "description = \"Audit code\"\nprompt = \"Review $ARGUMENTS\"\n",
		"commands/report.md":       "---\ndescription: Report results\n---\nSummarize $ARGUMENTS",
		"commands/broken.toml":     "prompt = [",
	}
	for name, body := range files {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	var system, user string
	eng, err := mow.New(mow.Options{Workspace: t.TempDir(), NoSession: true, LoadUserConfig: true, Chat: func(_ context.Context, messages []mow.Message, _ []mow.ToolSpec) (mow.Message, error) {
		for _, m := range messages {
			if m.Role == "system" {
				system = m.Content
			}
			if m.Role == "user" {
				user = m.Content
			}
		}
		return mow.Message{Role: "assistant", Content: "done"}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()
	states := eng.SkillStatuses()
	if states["baseline"] != "enabled (next prompt)" || states["audit"] != "available" {
		t.Fatal(states)
	}
	commands := map[string]bool{}
	for _, c := range eng.PluginCommands() {
		commands[c.Name] = true
	}
	if !commands["audit"] || !commands["review-kit:audit"] || !commands["report"] || commands["broken"] {
		t.Fatal(commands)
	}
	if _, err := eng.Prompt(t.Context(), "/review-kit:audit gateway"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(user, "Review gateway") || !strings.Contains(system, "AUDIT-INSTRUCTIONS") || !strings.Contains(system, "BASELINE") || strings.Contains(system, "UNUSED-INSTRUCTIONS") {
		t.Fatalf("user=%s system=%s", user, system)
	}
	states = eng.SkillStatuses()
	if states["audit"] != "loaded" || states["baseline"] != "loaded" || states["unused"] != "available" {
		t.Fatal(states)
	}
	if _, err := eng.Prompt(t.Context(), "/report findings"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(user, "Summarize findings") {
		t.Fatal(user)
	}
}
