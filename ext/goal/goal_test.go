package goal_test

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/subosito/mow"
	"github.com/subosito/mow/ext/goal"
)

func TestNormalizeAndSlug(t *testing.T) {
	s, err := goal.NormalizeSpec(goal.Spec{Goal: "Fix the CI pipeline now!"})
	if err != nil {
		t.Fatal(err)
	}
	if s.ID == "" || s.MaxSteps != 8 {
		t.Fatalf("%+v", s)
	}
	if _, err := goal.NormalizeSpec(goal.Spec{Goal: "x", ID: "../evil"}); err == nil {
		t.Fatal("expected bad id")
	}
}

func TestParseOutcome(t *testing.T) {
	done, fail, _ := goal.ParseOutcome("all good\nGOAL_DONE\n")
	if !done || fail {
		t.Fatal("done")
	}
	done, fail, reason := goal.ParseOutcome("nope\nGOAL_FAILED: no access\n")
	if done || !fail || reason != "no access" {
		t.Fatalf("%v %v %q", done, fail, reason)
	}
}

func TestStoreRoundTrip(t *testing.T) {
	dir := t.TempDir()
	st := goal.State{ID: "g1", Goal: "do it", Status: goal.StatusPending, MaxSteps: 3}
	s := &goal.Store{Dir: dir}
	if err := s.Save(st); err != nil {
		t.Fatal(err)
	}
	got, err := s.Load("g1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Goal != "do it" || got.Status != goal.StatusPending {
		t.Fatalf("%+v", got)
	}
	list, err := s.List()
	if err != nil || len(list) != 1 {
		t.Fatalf("list=%v err=%v", list, err)
	}
}

func TestRunnerCompletesOnMarker(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("MOW_HOME", dir)
	var n atomic.Int32
	eng, err := mow.New(mow.Options{
		NoSession: true,
		Chat: func(ctx context.Context, messages []mow.Message, tools []mow.ToolSpec) (mow.Message, error) {
			i := n.Add(1)
			if i < 2 {
				return mow.Message{Role: "assistant", Content: "working on it"}, nil
			}
			return mow.Message{Role: "assistant", Content: "finished\nGOAL_DONE"}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	var events []goal.EventKind
	r := &goal.Runner{
		Engine: eng,
		Store:  &goal.Store{Dir: dir + "/goals"},
		OnEvent: func(e goal.Event) {
			events = append(events, e.Kind)
		},
	}
	st, err := r.RunSpec(context.Background(), goal.Spec{
		ID: "ci", Goal: "ship it", MaxSteps: 5,
	})
	if err != nil {
		t.Fatal(err)
	}
	if st.Status != goal.StatusDone || st.Step != 2 {
		t.Fatalf("status=%s step=%d", st.Status, st.Step)
	}
	// start, step, done (or start, step1, done without intermediate if done on step 2 fires done only)
	var kinds string
	for _, k := range events {
		kinds += string(k) + ","
	}
	if !strings.Contains(kinds, string(goal.EventStart)) || !strings.Contains(kinds, string(goal.EventDone)) {
		t.Fatalf("events=%v", events)
	}
	// Persisted
	loaded, err := r.Store.Load("ci")
	if err != nil || loaded.Status != goal.StatusDone {
		t.Fatalf("load %+v err=%v", loaded, err)
	}
}

func TestRunnerFailsOnMarker(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("MOW_HOME", dir)
	eng, err := mow.New(mow.Options{
		NoSession: true,
		Chat: func(ctx context.Context, messages []mow.Message, tools []mow.ToolSpec) (mow.Message, error) {
			return mow.Message{Role: "assistant", Content: "GOAL_FAILED: blocked"}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	r := &goal.Runner{Engine: eng, Store: &goal.Store{Dir: dir + "/goals"}}
	st, err := r.RunSpec(context.Background(), goal.Spec{ID: "x", Goal: "y", MaxSteps: 3})
	if err == nil || st.Status != goal.StatusFailed {
		t.Fatalf("st=%+v err=%v", st, err)
	}
	if st.Error != "blocked" {
		t.Fatalf("error=%q", st.Error)
	}
}

func TestRunnerMaxSteps(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("MOW_HOME", dir)
	eng, err := mow.New(mow.Options{
		NoSession: true,
		Chat: func(ctx context.Context, messages []mow.Message, tools []mow.ToolSpec) (mow.Message, error) {
			return mow.Message{Role: "assistant", Content: "still going"}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	r := &goal.Runner{Engine: eng, Store: &goal.Store{Dir: dir + "/goals"}}
	st, err := r.RunSpec(context.Background(), goal.Spec{ID: "m", Goal: "g", MaxSteps: 2})
	if err == nil || st.Status != goal.StatusFailed || st.Step != 2 {
		t.Fatalf("st=%+v err=%v", st, err)
	}
}

func TestSubscribe(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("MOW_HOME", dir)
	var saw int
	unsub := goal.Subscribe(func(e goal.Event) {
		if e.Kind == goal.EventStart {
			saw++
		}
	})
	defer unsub()
	eng, err := mow.New(mow.Options{
		NoSession: true,
		Chat: func(ctx context.Context, messages []mow.Message, tools []mow.ToolSpec) (mow.Message, error) {
			return mow.Message{Role: "assistant", Content: "GOAL_DONE"}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	r := &goal.Runner{Engine: eng, Store: &goal.Store{Dir: dir + "/goals"}}
	if _, err := r.RunSpec(context.Background(), goal.Spec{ID: "s", Goal: "g"}); err != nil {
		t.Fatal(err)
	}
	if saw != 1 {
		t.Fatalf("subscribe saw %d", saw)
	}
}
