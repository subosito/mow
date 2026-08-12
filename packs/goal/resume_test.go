package goal

import (
	"context"
	"errors"
	"testing"

	"github.com/subosito/mow"
)

func TestApplyMaxStepsRaise(t *testing.T) {
	st := State{ID: "x", MaxSteps: 8, Step: 8, Status: StatusFailed, Error: "max steps 8 exceeded"}
	got := applyMaxStepsRaise(st, 8)
	if got.MaxSteps != 8 || got.Status != StatusFailed {
		t.Fatalf("no raise: %+v", got)
	}
	got = applyMaxStepsRaise(st, 24)
	if got.MaxSteps != 24 {
		t.Fatalf("max=%d", got.MaxSteps)
	}
	if got.Status != StatusPending || got.Error != "" {
		t.Fatalf("should clear max-steps failure: status=%s err=%q", got.Status, got.Error)
	}
	// Never lower.
	st2 := State{MaxSteps: 24, Status: StatusFailed, Error: "other"}
	got = applyMaxStepsRaise(st2, 8)
	if got.MaxSteps != 24 {
		t.Fatalf("must not lower: %d", got.MaxSteps)
	}
}

func TestRunRaiseContinuesPastOldCap(t *testing.T) {
	t.Setenv("MOW_HOME", t.TempDir())
	t.Setenv("OPENAI_API_KEY", "k")
	t.Setenv("OPENAI_MODEL", "m")
	dir := t.TempDir()
	store := &Store{Dir: dir}
	// Pretend we already used 8/8.
	st := State{
		ID: "cap", Goal: "finish", Status: StatusFailed,
		Step: 8, MaxSteps: 8, Error: "max steps 8 exceeded",
	}
	if err := store.Save(st); err != nil {
		t.Fatal(err)
	}
	n := 0
	eng, err := mow.New(mow.Options{
		NoSession: true,
		Chat: func(ctx context.Context, messages []mow.Message, tools []mow.ToolSpec) (mow.Message, error) {
			n++
			return mow.Message{Role: "assistant", Content: "done\nGOAL_DONE"}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	r := &Runner{Engine: eng, Store: store}
	out, err := r.RunRaise(context.Background(), "cap", 24)
	if err != nil {
		t.Fatalf("err=%v out=%+v", err, out)
	}
	if out.Status != StatusDone {
		t.Fatalf("status=%s step=%d/%d err=%q", out.Status, out.Step, out.MaxSteps, out.Error)
	}
	if out.MaxSteps != 24 || out.Step != 9 {
		t.Fatalf("step=%d max=%d (want step 9 after one more Prompt)", out.Step, out.MaxSteps)
	}
	if n != 1 {
		t.Fatalf("chats=%d want 1", n)
	}
}

func TestResumeAnswerDoesNotPersistUnownedRunning(t *testing.T) {
	t.Setenv("MOW_HOME", t.TempDir())
	store := &Store{Dir: t.TempDir()}
	if err := store.Save(State{
		ID: "esc", Goal: "g", Status: StatusBlocked,
		Question: "pick one", MaxSteps: 3,
	}); err != nil {
		t.Fatal(err)
	}
	release, err := acquireRunExclusive(store, "esc")
	if err != nil {
		t.Fatal(err)
	}
	defer release()

	eng, err := mow.New(mow.Options{
		NoSession: true,
		Chat: func(ctx context.Context, messages []mow.Message, tools []mow.ToolSpec) (mow.Message, error) {
			t.Fatal("chat must not run while the run lock is held")
			return mow.Message{}, errors.New("unreachable")
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	r := &Runner{Engine: eng, Store: store}
	_, err = r.ResumeAnswer(context.Background(), "esc", "use option B")
	if !errors.Is(err, ErrGoalAlreadyRunning) {
		t.Fatalf("got %v, want ErrGoalAlreadyRunning", err)
	}
	got, err := store.Load("esc")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != StatusBlocked {
		t.Fatalf("status=%s want blocked (answer must not leave unowned running)", got.Status)
	}
	if got.RunOwnerPID != 0 {
		t.Fatalf("owner pid=%d, want 0", got.RunOwnerPID)
	}
	if got.Question != "pick one" {
		t.Fatalf("question=%q, want original (lock failed before persist)", got.Question)
	}
}
