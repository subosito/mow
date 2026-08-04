package mow

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResumeRestoresSessionEffort(t *testing.T) {
	home := t.TempDir()
	ws := t.TempDir()
	t.Setenv("MOW_HOME", home)

	newEng := func(model, effort, sid string, explicitModel, explicitEffort bool) (*Engine, error) {
		return New(Options{
			Workspace:      ws,
			Model:          model,
			Effort:         effort,
			ExplicitModel:  explicitModel,
			ExplicitEffort: explicitEffort,
			SessionID:      sid,
			Chat: func(context.Context, []Message, []ToolSpec) (Message, error) {
				return Message{Role: "assistant", Content: "ok"}, nil
			},
		})
	}

	// Turn 1: session records effort "high".
	eng1, err := newEng("model-a", "high", "sess-effort", false, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := eng1.Prompt(context.Background(), "hello"); err != nil {
		t.Fatal(err)
	}
	eng1.Close()

	// Resume with a config default (lower) effort → session effort wins.
	eng2, err := newEng("model-a", "low", "sess-effort", false, false)
	if err != nil {
		t.Fatal(err)
	}
	if got := eng2.Effort(); got != "high" {
		t.Fatalf("Effort() = %q, want high (session effort should win over config default)", got)
	}
	eng2.Close()

	// Resume with an explicit --effort → explicit wins.
	eng3, err := newEng("model-a", "low", "sess-effort", false, true)
	if err != nil {
		t.Fatal(err)
	}
	if got := eng3.Effort(); got != "low" {
		t.Fatalf("Effort() = %q, want low (explicit --effort must win)", got)
	}
	eng3.Close()
}

func TestResumeSessionEffortPersistedInRuntimeEvent(t *testing.T) {
	home := t.TempDir()
	ws := t.TempDir()
	t.Setenv("MOW_HOME", home)

	eng, err := New(Options{
		Workspace: ws,
		Model:     "model-a",
		Effort:    "medium",
		SessionID: "sess-effort-file",
		Chat: func(context.Context, []Message, []ToolSpec) (Message, error) {
			return Message{Role: "assistant", Content: "ok"}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := eng.Prompt(context.Background(), "hello"); err != nil {
		t.Fatal(err)
	}
	eng.Close()

	// The runtime event must carry the effort for future resumes.
	raw := []byte(nil)
	_ = filepath.WalkDir(home, func(p string, d os.DirEntry, _ error) error {
		if d != nil && !d.IsDir() && strings.HasSuffix(p, "sess-effort-file.jsonl") {
			raw, _ = os.ReadFile(p)
		}
		return nil
	})
	if raw == nil {
		t.Fatal("session file not found under MOW_HOME")
	}
	if !strings.Contains(string(raw), `"effort":"medium"`) {
		t.Fatalf("session runtime event missing effort: %s", raw)
	}
}
