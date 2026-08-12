package goal

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/subosito/mow"
)

func TestGoalBridgeEmitsMowEngineEvents(t *testing.T) {
	t.Setenv("MOW_HOME", t.TempDir())
	t.Setenv("OPENAI_API_KEY", "k")
	t.Setenv("OPENAI_MODEL", "m")
	dir := t.TempDir()

	var events []mow.Event
	var mu sync.Mutex
	eng, err := mow.New(mow.Options{
		Workspace: t.TempDir(),
		NoSession: true,
		OnEvent: func(ev mow.Event) {
			if strings.HasPrefix(string(ev.Type), "graph.goal.") {
				mu.Lock()
				events = append(events, ev)
				mu.Unlock()
			}
		},
		Chat: func(ctx context.Context, messages []mow.Message, tools []mow.ToolSpec) (mow.Message, error) {
			return mow.Message{Role: "assistant", Content: "done\nGOAL_DONE"}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()

	r := &Runner{Engine: eng, Store: &Store{Dir: dir}}
	st, err := r.RunSpec(context.Background(), Spec{ID: "bridge-test", Goal: "test bridge", MaxSteps: 2})
	if err != nil {
		t.Fatalf("err=%v st=%+v", err, st)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(events) < 2 {
		t.Fatalf("events count=%d, want at least start and done", len(events))
	}
	if events[0].Type != mow.EventGoalStart || events[0].Goal == nil || events[0].Goal.ID != "bridge-test" {
		t.Fatalf("first event invalid: %+v", events[0])
	}
	last := events[len(events)-1]
	if last.Type != mow.EventGoalDone || last.Goal == nil || last.Goal.Status != "done" {
		t.Fatalf("last event invalid: %+v", last)
	}
}

func TestIsTransientLLM(t *testing.T) {
	if !isTransientLLM(fmt.Errorf("llm: HTTP 502: upstream error")) {
		t.Fatal("502")
	}
	if !isTransientLLM(fmt.Errorf("llm: HTTP 503: service unavailable")) {
		t.Fatal("503")
	}
	if isTransientLLM(fmt.Errorf("llm: HTTP 400: bad request")) {
		t.Fatal("400 should not be transient")
	}
}

func TestRunnerSoftRecoversTransientLLMFast(t *testing.T) {
	old := transientBackoff
	transientBackoff = 0
	defer func() { transientBackoff = old }()

	t.Setenv("MOW_HOME", t.TempDir())
	t.Setenv("OPENAI_API_KEY", "k")
	t.Setenv("OPENAI_MODEL", "m")
	dir := t.TempDir()
	var chats atomic.Int32
	eng, err := mow.New(mow.Options{
		Workspace: t.TempDir(),
		NoSession: true,
		Chat: func(ctx context.Context, messages []mow.Message, tools []mow.ToolSpec) (mow.Message, error) {
			n := chats.Add(1)
			if n == 1 {
				return mow.Message{}, fmt.Errorf("llm: HTTP 502: upstream error")
			}
			return mow.Message{Role: "assistant", Content: "recovered\nGOAL_DONE"}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	r := &Runner{Engine: eng, Store: &Store{Dir: dir}}
	st, err := r.RunSpec(context.Background(), Spec{ID: "t502", Goal: "survive blip", MaxSteps: 5})
	if err != nil {
		t.Fatalf("err=%v st=%+v", err, st)
	}
	if st.Status != StatusDone || st.Step != 2 {
		t.Fatalf("status=%s step=%d error=%q", st.Status, st.Step, st.Error)
	}
}

func TestRunnerFailsAfterRepeatedTransientLLMFast(t *testing.T) {
	old := transientBackoff
	transientBackoff = time.Millisecond
	defer func() { transientBackoff = old }()

	t.Setenv("MOW_HOME", t.TempDir())
	t.Setenv("OPENAI_API_KEY", "k")
	t.Setenv("OPENAI_MODEL", "m")
	dir := t.TempDir()
	eng, err := mow.New(mow.Options{
		Workspace: t.TempDir(),
		NoSession: true,
		Chat: func(ctx context.Context, messages []mow.Message, tools []mow.ToolSpec) (mow.Message, error) {
			return mow.Message{}, fmt.Errorf("llm: HTTP 502: upstream error")
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	r := &Runner{Engine: eng, Store: &Store{Dir: dir}}
	st, err := r.RunSpec(context.Background(), Spec{ID: "t502b", Goal: "die", MaxSteps: 8})
	if err == nil || st.Status != StatusFailed {
		t.Fatalf("st=%+v err=%v want failed", st, err)
	}
	if st.Step != 5 {
		t.Fatalf("step=%d want 5 (maxTransientSteps)", st.Step)
	}
	if !strings.Contains(st.Error, "502") && !strings.Contains(st.Error, "upstream") {
		t.Fatalf("error=%q", st.Error)
	}
}
