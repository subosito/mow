package mow_test

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/subosito/mow"
)

func TestPromptEventsRunLifecycle(t *testing.T) {
	var mu sync.Mutex
	var types []string
	eng, err := mow.New(mow.Options{
		NoSession: true,
		Chat: func(ctx context.Context, messages []mow.Message, tools []mow.ToolSpec) (mow.Message, error) {
			return mow.Message{Role: "assistant", Content: "ok"}, nil
		},
		OnEvent: func(ev mow.Event) {
			mu.Lock()
			types = append(types, string(ev.Type))
			mu.Unlock()
			if ev.RunID == "" {
				t.Error("empty run_id")
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	res, err := eng.Prompt(context.Background(), "hi")
	if err != nil {
		t.Fatal(err)
	}
	if res.RunID == "" || res.StopReason != mow.StopCompleted {
		t.Fatalf("result=%+v", res)
	}
	mu.Lock()
	defer mu.Unlock()
	joined := strings.Join(types, ",")
	if !strings.Contains(joined, "run.start") || !strings.Contains(joined, "run.end") {
		t.Fatalf("events=%v", types)
	}
	if !strings.Contains(joined, "turn") {
		t.Fatalf("missing turn event: %v", types)
	}
}

func TestCancelInFlightPrompt(t *testing.T) {
	started := make(chan struct{})
	eng, err := mow.New(mow.Options{
		NoSession: true,
		Chat: func(ctx context.Context, messages []mow.Message, tools []mow.ToolSpec) (mow.Message, error) {
			close(started)
			<-ctx.Done()
			return mow.Message{}, ctx.Err()
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan mow.RunResult, 1)
	errCh := make(chan error, 1)
	go func() {
		res, err := eng.Prompt(context.Background(), "block")
		done <- res
		errCh <- err
	}()
	<-started
	if !eng.Status().Busy {
		t.Fatal("expected busy")
	}
	eng.Cancel()
	res := <-done
	err = <-errCh
	if err == nil {
		t.Fatal("expected cancel error")
	}
	if res.StopReason != mow.StopCancelled {
		t.Fatalf("stop=%q", res.StopReason)
	}
}

func TestOnEventFanOut(t *testing.T) {
	var a, b int
	eng, err := mow.New(mow.Options{
		NoSession: true,
		Chat: func(ctx context.Context, messages []mow.Message, tools []mow.ToolSpec) (mow.Message, error) {
			return mow.Message{Role: "assistant", Content: "x"}, nil
		},
		OnEvent: func(ev mow.Event) {
			if ev.Type == mow.EventRunStart {
				a++
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	unsub := eng.AddOnEvent(func(ev mow.Event) {
		if ev.Type == mow.EventRunStart {
			b++
		}
	})
	if _, err := eng.Prompt(context.Background(), "hi"); err != nil {
		t.Fatal(err)
	}
	if a != 1 || b != 1 {
		t.Fatalf("a=%d b=%d want both 1", a, b)
	}
	unsub()
	if _, err := eng.Prompt(context.Background(), "again"); err != nil {
		t.Fatal(err)
	}
	if a != 2 || b != 1 {
		t.Fatalf("after unsub a=%d b=%d", a, b)
	}
}

func TestEngineFromContextDuringPrompt(t *testing.T) {
	var saw bool
	eng, err := mow.New(mow.Options{
		NoSession: true,
		Chat: func(ctx context.Context, messages []mow.Message, tools []mow.ToolSpec) (mow.Message, error) {
			if mow.EngineFromContext(ctx) == nil {
				t.Error("missing engine in ctx")
			} else {
				saw = true
			}
			return mow.Message{Role: "assistant", Content: "x"}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := eng.Prompt(context.Background(), "hi"); err != nil {
		t.Fatal(err)
	}
	if !saw {
		t.Fatal("engine not in context")
	}
}
