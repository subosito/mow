package engine

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func profileChat() func(context.Context, []Message, []ToolSpec) (Message, error) {
	return func(context.Context, []Message, []ToolSpec) (Message, error) {
		return Message{Role: "assistant", Content: "ok"}, nil
	}
}

// writeWorkspaceProfile creates the operator-controlled profile layout. Its
// files deliberately live below MOW_HOME rather than a checkout: selecting a
// profile must never cause a workspace to grant itself capabilities.
func writeWorkspaceProfile(t *testing.T, home, name, body string) string {
	t.Helper()
	profile := filepath.Join(home, "workspaces", name)
	if err := os.MkdirAll(profile, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(profile, "workspace.yaml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return profile
}

func TestWorkspaceProfileNameResolvesWorkspaceAndRoots(t *testing.T) {
	home := t.TempDir()
	workspace := t.TempDir()
	extra := t.TempDir()
	readOnly := t.TempDir()
	t.Setenv("MOW_HOME", home)
	writeWorkspaceProfile(t, home, "monorepo", "root: "+workspace+"\nextra_roots:\n  - "+extra+"\n  - "+readOnly+":ro\n")

	eng, err := New(Options{Workspace: "monorepo", Model: "model-a", NoSession: true, Chat: profileChat()})
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()
	if got := eng.Workspace(); got != workspace {
		t.Fatalf("Workspace() = %q, want %q", got, workspace)
	}
	if got := eng.ExtraRoots(); len(got) != 1 || got[0] != extra {
		t.Fatalf("ExtraRoots() = %v, want [%s]", got, extra)
	}
	if got := eng.ExtraRootsReadOnly(); len(got) != 1 || got[0] != readOnly {
		t.Fatalf("ExtraRootsReadOnly() = %v, want [%s]", got, readOnly)
	}
}

func TestWorkspaceProfileRejectsUnsafeNamesBeforePathResolution(t *testing.T) {
	home := t.TempDir()
	t.Setenv("MOW_HOME", home)
	for _, name := range []string{"team/api", `team\\api`, " team", "team ", "team..api"} {
		t.Run(strings.ReplaceAll(name, "/", "slash"), func(t *testing.T) {
			_, err := New(Options{Workspace: name, Model: "model-a", NoSession: true, Chat: profileChat()})
			if err == nil || !strings.Contains(strings.ToLower(err.Error()), "profile") {
				t.Fatalf("Workspace=%q error = %v, want profile-name validation error", name, err)
			}
		})
	}
}

func TestWorkspaceProfileConfigOverridesUserConfigButNotOptions(t *testing.T) {
	home := t.TempDir()
	workspace := t.TempDir()
	t.Setenv("MOW_HOME", home)
	if err := os.WriteFile(filepath.Join(home, "config.yaml"), []byte("llm:\n  model: user-model\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	profile := writeWorkspaceProfile(t, home, "monorepo", "root: "+workspace+"\n")
	if err := os.WriteFile(filepath.Join(profile, "config.yaml"), []byte("llm:\n  model: profile-model\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	fromProfile, err := New(Options{Workspace: "monorepo", NoSession: true, Chat: profileChat()})
	if err != nil {
		t.Fatal(err)
	}
	defer fromProfile.Close()
	if got := fromProfile.Model(); got != "profile-model" {
		t.Fatalf("Model() = %q, want profile-model (profile config overrides user config)", got)
	}

	fromOption, err := New(Options{Workspace: "monorepo", Model: "option-model", NoSession: true, Chat: profileChat()})
	if err != nil {
		t.Fatal(err)
	}
	defer fromOption.Close()
	if got := fromOption.Model(); got != "option-model" {
		t.Fatalf("Model() = %q, want option-model (Options overrides profile config)", got)
	}
}

func TestWorkspaceProfileSkillsPrecedeTrustedWorkspaceSkills(t *testing.T) {
	home := t.TempDir()
	workspace := t.TempDir()
	t.Setenv("MOW_HOME", home)
	t.Setenv("MOW_TRUST_PROJECT", "1")
	profile := writeWorkspaceProfile(t, home, "monorepo", "root: "+workspace+"\n")
	for path, body := range map[string]string{
		filepath.Join(profile, "skills", "review", "SKILL.md"):           "PROFILE_REVIEW_SKILL",
		filepath.Join(workspace, ".mow", "skills", "review", "SKILL.md"): "WORKSPACE_REVIEW_SKILL",
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	var system string
	eng, err := New(Options{Workspace: "monorepo", NoSession: true, Chat: func(_ context.Context, messages []Message, _ []ToolSpec) (Message, error) {
		for _, message := range messages {
			if message.Role == "system" {
				system = message.Content
			}
		}
		return Message{Role: "assistant", Content: "ok"}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()
	if _, err := eng.Prompt(context.Background(), "review this change"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(system, "PROFILE_REVIEW_SKILL") || strings.Contains(system, "WORKSPACE_REVIEW_SKILL") {
		t.Fatalf("system prompt did not retain profile skill precedence: %q", system)
	}
}

func TestWorkspacePathStillWorksWithoutProfileLookup(t *testing.T) {
	home := t.TempDir()
	workspace := t.TempDir()
	t.Setenv("MOW_HOME", home)
	eng, err := New(Options{Workspace: workspace, Model: "model-a", NoSession: true, Chat: profileChat()})
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()
	if got := eng.Workspace(); got != workspace {
		t.Fatalf("Workspace() = %q, want plain path %q", got, workspace)
	}
}

func TestWorkspaceProfileDoesNotLoadLegacyWorkspaceSets(t *testing.T) {
	home := t.TempDir()
	legacyWorkspace := t.TempDir()
	profileWorkspace := t.TempDir()
	t.Setenv("MOW_HOME", home)
	if err := os.WriteFile(filepath.Join(home, "workspaces.yaml"), []byte("workspaces:\n  monorepo:\n    root: "+legacyWorkspace+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeWorkspaceProfile(t, home, "monorepo", "root: "+profileWorkspace+"\n")

	eng, err := New(Options{Workspace: "monorepo", Model: "model-a", NoSession: true, Chat: profileChat()})
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()
	if got := eng.Workspace(); got != profileWorkspace {
		t.Fatalf("Workspace() = %q, want profile root %q; legacy workspaces.yaml must be ignored", got, profileWorkspace)
	}
}
