package engine

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
			LoadUserConfig: true,
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

func TestSetEffortPersistsRuntimeEvent(t *testing.T) {
	home := t.TempDir()
	ws := t.TempDir()
	t.Setenv("MOW_HOME", home)

	newEng := func(effort string, explicit bool) (*Engine, error) {
		return New(Options{
			LoadUserConfig: true,
			Workspace:      ws,
			Model:          "model-a",
			Effort:         effort,
			ExplicitEffort: explicit,
			SessionID:      "sess-seteffort",
			Chat: func(context.Context, []Message, []ToolSpec) (Message, error) {
				return Message{Role: "assistant", Content: "ok"}, nil
			},
		})
	}

	// No Prompt: an effort change must reach the session file on its own,
	// or quitting right after /effort resumes a stale tier.
	eng1, err := newEng("", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := eng1.SetEffort("high"); err != nil {
		t.Fatal(err)
	}
	eng1.Close()

	raw := []byte(nil)
	_ = filepath.WalkDir(home, func(p string, d os.DirEntry, _ error) error {
		if d != nil && !d.IsDir() && strings.HasSuffix(p, "sess-seteffort.jsonl") {
			raw, _ = os.ReadFile(p)
		}
		return nil
	})
	if raw == nil {
		t.Fatal("session file not found under MOW_HOME")
	}
	if !strings.Contains(string(raw), `"effort":"high"`) {
		t.Fatalf("SetEffort did not persist a runtime event: %s", raw)
	}

	// Resume with a config default effort → the SetEffort value wins.
	eng2, err := newEng("low", false)
	if err != nil {
		t.Fatal(err)
	}
	if got := eng2.Effort(); got != "high" {
		t.Fatalf("Effort() = %q, want high (persisted by SetEffort without a prompt)", got)
	}
	eng2.Close()
}

func TestResumeLegacySessionKeepsConfiguredEffort(t *testing.T) {
	home := t.TempDir()
	ws := t.TempDir()
	t.Setenv("MOW_HOME", home)

	chat := func(context.Context, []Message, []ToolSpec) (Message, error) {
		return Message{Role: "assistant", Content: "ok"}, nil
	}
	// Materialize a session file, then strip runtime/snapshot events so it
	// looks like a file written before effort persistence existed.
	eng1, err := New(Options{
		LoadUserConfig: true,
		Workspace:      ws,
		Model:          "model-a",
		Effort:         "high",
		ExplicitEffort: true,
		SessionID:      "sess-legacy",
		Chat:           chat,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := eng1.Prompt(context.Background(), "hello"); err != nil {
		t.Fatal(err)
	}
	eng1.Close()

	var path string
	_ = filepath.WalkDir(home, func(p string, d os.DirEntry, _ error) error {
		if d != nil && !d.IsDir() && strings.HasSuffix(p, "sess-legacy.jsonl") {
			path = p
		}
		return nil
	})
	if path == "" {
		t.Fatal("session file not found under MOW_HOME")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var kept []string
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.Contains(line, `"type":"runtime"`) || strings.Contains(line, `"type":"snapshot"`) {
			continue
		}
		if strings.TrimSpace(line) != "" {
			kept = append(kept, line)
		}
	}
	if len(kept) == 0 {
		t.Fatal("no legacy events left after filtering")
	}
	if err := os.WriteFile(path, []byte(strings.Join(kept, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Resume the legacy file: no runtime metadata, so the configured effort
	// must survive unchanged (no restore, no error).
	eng2, err := New(Options{
		LoadUserConfig: true,
		Workspace:      ws,
		Model:          "model-a",
		Effort:         "low",
		SessionID:      "sess-legacy",
		Chat:           chat,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := eng2.Effort(); got != "low" {
		t.Fatalf("Effort() = %q, want low (legacy session without runtime events keeps configured effort)", got)
	}
	eng2.Close()
}

func TestResumeSessionEffortPersistedInRuntimeEvent(t *testing.T) {
	home := t.TempDir()
	ws := t.TempDir()
	t.Setenv("MOW_HOME", home)

	eng, err := New(Options{
		LoadUserConfig: true,
		Workspace:      ws,
		Model:          "model-a",
		Effort:         "medium",
		SessionID:      "sess-effort-file",
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
