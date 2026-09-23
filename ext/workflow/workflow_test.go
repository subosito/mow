package workflow

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/subosito/mow"
)

func unique(name string) string { return name + "-" + filepath.Base(os.TempDir()) }

func TestDefinitionRegistryAndSequentialExecution(t *testing.T) {
	name := "test-sequential"
	_ = Register(Definition{Name: name, Version: "1", MaxBytes: 1024, Phases: []Phase{
		{ID: "prompt", Kind: PhasePrompt, Effect: EffectReadOnly, Prompt: &PromptSpec{Prompt: "Return JSON", ReadOnly: true, AllowedTools: []string{"read"}}},
		{ID: "verify", Kind: PhaseVerify, Effect: EffectReadOnly, Verify: func(_ context.Context, in json.RawMessage) (json.RawMessage, error) {
			if string(in) != `{"value":1}` {
				t.Fatalf("verify input=%s", in)
			}
			return json.RawMessage(`{"ok":true}`), nil
		}},
	}})
	eng, err := mow.New(mow.Options{Workspace: t.TempDir(), NoSession: true, Chat: func(_ context.Context, msgs []mow.Message, tools []mow.ToolSpec) (mow.Message, error) {
		for _, tool := range tools {
			if tool.Function.Name != "read" {
				t.Fatalf("unexpected tool %s", tool.Function.Name)
			}
		}
		return mow.Message{Role: "assistant", Content: `{"value":1}`}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()
	store := &Store{Dir: t.TempDir()}
	var events []Event
	st, err := (&Runner{Engine: eng, Store: store, OnEvent: func(e Event) { events = append(events, e) }}).Run(t.Context(), "run-1", name, "1", json.RawMessage(`{"seed":1}`))
	if err != nil || st.Status != StatusSucceeded || len(st.Phases) != 2 || string(st.Phases[1].Output) != `{"ok":true}` {
		t.Fatalf("state=%+v err=%v", st, err)
	}
	if len(events) < 6 || events[0].Type != EventRunStart || events[len(events)-1].Type != EventRunDone {
		t.Fatalf("events=%+v", events)
	}
	if _, err := (&Runner{Engine: eng, Store: store}).Run(t.Context(), "run-1", name, "1", json.RawMessage(`{"seed":1}`)); !errors.Is(err, ErrExists) {
		t.Fatalf("duplicate run err=%v", err)
	}
}

func TestDefinitionValidationAndDefensiveCopy(t *testing.T) {
	name := "copy-test"
	tools := []string{"read"}
	d := Definition{Name: name, Version: "1", Phases: []Phase{{ID: "p", Kind: PhasePrompt, Effect: EffectReadOnly, Prompt: &PromptSpec{Prompt: "x", ReadOnly: true, AllowedTools: tools}}}}
	if err := Register(d); err != nil {
		t.Fatal(err)
	}
	tools[0] = "bash"
	got, ok := Lookup(name, "1")
	if !ok || got.Phases[0].Prompt.AllowedTools[0] != "read" {
		t.Fatal("registered definition mutated")
	}
	got.Phases[0].Prompt.AllowedTools[0] = "write"
	again, _ := Lookup(name, "1")
	if again.Phases[0].Prompt.AllowedTools[0] != "read" {
		t.Fatal("lookup leaked mutable slice")
	}
	if err := Register(d); err == nil {
		t.Fatal("duplicate accepted")
	}
	bad := []Definition{
		{Name: "bad/name", Version: "1", Phases: []Phase{{ID: "v", Kind: PhaseVerify, Verify: func(context.Context, json.RawMessage) (json.RawMessage, error) { return json.RawMessage(`{}`), nil }}}},
		{Name: "bad", Version: "1", Phases: []Phase{{ID: "p", Kind: PhasePrompt, Effect: EffectReadOnly, Prompt: &PromptSpec{Prompt: "x", ReadOnly: false}}}},
		{Name: "bad2", Version: "1", Phases: []Phase{{ID: "p", Kind: PhasePrompt, Prompt: &PromptSpec{Prompt: "x"}, Verify: func(context.Context, json.RawMessage) (json.RawMessage, error) { return nil, nil }}}},
	}
	for _, d := range bad {
		if Register(d) == nil {
			t.Fatal("invalid definition accepted")
		}
	}
}

func TestStoreStrictAtomicAndCrashReleasedLock(t *testing.T) {
	store := &Store{Dir: filepath.Join(t.TempDir(), "runs")}
	st := State{ID: "run-2", Definition: "wf", Version: "1", Status: StatusPending, Input: json.RawMessage(`{}`), Phases: []PhaseState{{ID: "v", Kind: PhaseVerify, Status: PhasePending}}, CreatedAt: time.Now().UTC()}
	if err := store.Save(st); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(store.Path(st.ID)); err != nil || info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("state mode=%v err=%v", info, err)
	}
	if _, err := store.Load("../escape"); err == nil {
		t.Fatal("unsafe id accepted")
	}
	if err := os.WriteFile(store.Path(st.ID), []byte(`{"unknown":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(st.ID); !errors.Is(err, ErrInvalid) {
		t.Fatalf("corrupt state err=%v", err)
	}
}

func TestResumeBlocksInterruptedPhaseWithoutReexecution(t *testing.T) {
	name := "interrupted"
	calls := 0
	_ = Register(Definition{Name: name, Version: "1", Phases: []Phase{{ID: "verify", Kind: PhaseVerify, Verify: func(context.Context, json.RawMessage) (json.RawMessage, error) {
		calls++
		return json.RawMessage(`{}`), nil
	}}}})
	store := &Store{Dir: t.TempDir()}
	st := State{ID: "run-3", Definition: name, Version: "1", Status: StatusRunning, CurrentPhase: "verify", Input: json.RawMessage(`{}`), Phases: []PhaseState{{ID: "verify", Kind: PhaseVerify, Status: PhaseRunning, Attempts: 1}}, CreatedAt: time.Now().UTC()}
	if err := store.Save(st); err != nil {
		t.Fatal(err)
	}
	got, err := (&Runner{Store: store}).Resume(t.Context(), st.ID)
	if !errors.Is(err, ErrBlocked) || got.Status != StatusBlocked || got.Phases[0].Status != PhaseBlocked || calls != 0 {
		t.Fatalf("state=%+v err=%v calls=%d", got, err, calls)
	}
}

func TestPhaseFailurePersistsTypedCode(t *testing.T) {
	name := "verify-fail"
	_ = Register(Definition{Name: name, Version: "1", Phases: []Phase{{ID: "verify", Kind: PhaseVerify, Verify: func(context.Context, json.RawMessage) (json.RawMessage, error) {
		return nil, errors.New("private provider text")
	}}}})
	store := &Store{Dir: t.TempDir()}
	st, err := (&Runner{Store: store}).Run(t.Context(), "run-4", name, "1", json.RawMessage(`{}`))
	if err == nil || st.Status != StatusFailed || st.Phases[0].ErrorCode != "verification_failed" {
		t.Fatalf("state=%+v err=%v", st, err)
	}
	loaded, err := store.Load(st.ID)
	if err != nil || loaded.Phases[0].ErrorCode != "verification_failed" {
		t.Fatalf("loaded=%+v err=%v", loaded, err)
	}
	raw, _ := os.ReadFile(store.Path(st.ID))
	if bytes.Contains(raw, []byte("private provider text")) {
		t.Fatal("raw error persisted")
	}
}
