package goal

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/subosito/mow"
)

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
