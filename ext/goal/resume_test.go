package goal

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

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

// doneEngine returns a hermetic engine whose model finishes on the first step.
func doneEngine(t *testing.T) *mow.Engine {
	t.Helper()
	t.Setenv("MOW_HOME", t.TempDir())
	t.Setenv("OPENAI_API_KEY", "k")
	t.Setenv("OPENAI_MODEL", "m")
	eng, err := mow.New(mow.Options{
		NoSession: true,
		Chat: func(ctx context.Context, messages []mow.Message, tools []mow.ToolSpec) (mow.Message, error) {
			return mow.Message{Role: "assistant", Content: "done\nGOAL_DONE"}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = eng.Close() })
	return eng
}

func TestRunSpecResumesPartialKeepsProgress(t *testing.T) {
	store := &Store{Dir: t.TempDir()}
	if err := store.Save(State{
		ID: "p", Goal: "g", Status: StatusPartial, Partial: "done 1/2", Error: "stopped partial by route",
		Step: 3, MaxSteps: 8,
		Facts: []Fact{{Claim: "kept", ProducedByStep: 1}},
	}); err != nil {
		t.Fatal(err)
	}
	r := &Runner{Engine: doneEngine(t), Store: store}
	out, err := r.RunSpec(context.Background(), Spec{ID: "p", Goal: "g"})
	if err != nil {
		t.Fatalf("err=%v out=%+v", err, out)
	}
	if out.Status != StatusDone || out.Step != 4 || len(out.Facts) == 0 {
		t.Fatalf("partial must resume, not restart: %+v", out)
	}
	if out.Partial != "" {
		t.Fatalf("partial marker not cleared: %q", out.Partial)
	}
}

func TestRunSpecRefusesBlockedWithoutAnswer(t *testing.T) {
	store := &Store{Dir: t.TempDir()}
	if err := store.Save(State{
		ID: "b", Goal: "g", Status: StatusBlocked, Question: "which db?", Step: 2, MaxSteps: 8,
	}); err != nil {
		t.Fatal(err)
	}
	r := &Runner{Engine: doneEngine(t), Store: store}
	_, err := r.RunSpec(context.Background(), Spec{ID: "b", Goal: "g"})
	if err == nil || !strings.Contains(err.Error(), "--answer") {
		t.Fatalf("err=%v", err)
	}
	got, _ := store.Load("b")
	if got.Status != StatusBlocked || got.Step != 2 || got.Question != "which db?" {
		t.Fatalf("blocked goal overwritten: %+v", got)
	}
}

func TestRunSpecDefaultMaxStepsDoesNotRaiseStored(t *testing.T) {
	store := &Store{Dir: t.TempDir()}
	if err := store.Save(State{ID: "m", Goal: "g", Status: StatusPending, MaxSteps: 8}); err != nil {
		t.Fatal(err)
	}
	r := &Runner{Engine: doneEngine(t), Store: store}
	// MaxSteps 0 = "not passed": stored 8 stays, even though the default is 16.
	out, err := r.RunSpec(context.Background(), Spec{ID: "m", Goal: "g"})
	if err != nil {
		t.Fatal(err)
	}
	if out.MaxSteps != 8 {
		t.Fatalf("max_steps=%d want stored 8", out.MaxSteps)
	}
	// Explicit raise still works.
	if err := store.Save(State{ID: "m2", Goal: "g", Status: StatusPending, MaxSteps: 8}); err != nil {
		t.Fatal(err)
	}
	out, err = r.RunSpec(context.Background(), Spec{ID: "m2", Goal: "g", MaxSteps: 24})
	if err != nil {
		t.Fatal(err)
	}
	if out.MaxSteps != 24 {
		t.Fatalf("max_steps=%d want 24", out.MaxSteps)
	}
}

func TestRunStartedAtIsPerRunAndResetClearsRunFields(t *testing.T) {
	store := &Store{Dir: t.TempDir()}
	old := time.Now().Add(-48 * time.Hour).UTC()
	if err := store.Save(State{
		ID: "d", Goal: "g", Status: StatusPending, Step: 1, MaxSteps: 8,
		StartedAt: old, MaxDurationMs: int64(time.Hour / time.Millisecond),
		InputTokens: 5, RetryCount: 2, Partial: "x", Question: "q",
	}); err != nil {
		t.Fatal(err)
	}
	r := &Runner{Engine: doneEngine(t), Store: store}
	out, err := r.Run(context.Background(), "d")
	if err != nil {
		t.Fatalf("day-later resume must not trip MaxDuration: %v (%+v)", err, out)
	}
	if out.Status != StatusDone || !out.StartedAt.After(old) {
		t.Fatalf("%+v", out)
	}
	reset, err := store.Reset("d")
	if err != nil {
		t.Fatal(err)
	}
	if !reset.StartedAt.IsZero() || reset.InputTokens != 0 || reset.RetryCount != 0 || reset.Partial != "" || reset.Question != "" {
		t.Fatalf("reset left run fields: %+v", reset)
	}
}

func TestRetryCountResetsOnProgress(t *testing.T) {
	t.Setenv("MOW_HOME", t.TempDir())
	t.Setenv("OPENAI_API_KEY", "k")
	t.Setenv("OPENAI_MODEL", "m")
	store := &Store{Dir: t.TempDir()}
	n := 0
	eng, err := mow.New(mow.Options{
		NoSession: true,
		Chat: func(ctx context.Context, messages []mow.Message, tools []mow.ToolSpec) (mow.Message, error) {
			n++
			if n < 3 {
				return mow.Message{Role: "assistant", Content: "working"}, nil
			}
			return mow.Message{Role: "assistant", Content: "done\nGOAL_DONE"}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()
	// Verify flags the first step only, forcing one retry_same; the following
	// continue step must zero the streak so the Partial cap is not sticky.
	verified := false
	r := &Runner{Engine: eng, Store: store, Verify: func(st State, sr StepResult) []string {
		if !verified {
			verified = true
			return []string{"first step rejected"}
		}
		return nil
	}}
	var seen []int
	r.OnEvent = func(e Event) { seen = append(seen, e.State.RetryCount) }
	out, err := r.RunSpec(context.Background(), Spec{ID: "rc", Goal: "g", MaxSteps: 8})
	if err != nil {
		t.Fatalf("err=%v out=%+v", err, out)
	}
	if out.RetryCount != 0 {
		t.Fatalf("retry_count=%d after done, want 0 (events=%v)", out.RetryCount, seen)
	}
	peak := 0
	for _, c := range seen {
		peak = max(c, peak)
	}
	if peak != 1 {
		t.Fatalf("expected exactly one retry_same then reset; retry counts seen=%v", seen)
	}
}
