package goal_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/subosito/mow"
	"github.com/subosito/mow/packs/goal"
)

func TestRunnerRejectsConcurrentSameID(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("MOW_HOME", dir)
	block := make(chan struct{})
	eng, err := mow.New(mow.Options{
		NoSession: true,
		Chat: func(ctx context.Context, messages []mow.Message, tools []mow.ToolSpec) (mow.Message, error) {
			select {
			case <-block:
				return mow.Message{Role: "assistant", Content: "working"}, nil
			case <-ctx.Done():
				return mow.Message{}, ctx.Err()
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	store := &goal.Store{Dir: dir + "/goals"}
	r := &goal.Runner{Engine: eng, Store: store}
	spec := goal.Spec{ID: "dup", Goal: "work", MaxSteps: 8}

	var wg sync.WaitGroup
	errCh := make(chan error, 2)
	wg.Add(2)
	for i := 0; i < 2; i++ {
		go func() {
			defer wg.Done()
			_, err := r.RunSpec(context.Background(), spec)
			errCh <- err
		}()
	}
	time.Sleep(100 * time.Millisecond)
	close(block)
	wg.Wait()
	close(errCh)
	var sawAlready bool
	for err := range errCh {
		if errors.Is(err, goal.ErrGoalAlreadyRunning) {
			sawAlready = true
		}
	}
	if !sawAlready {
		t.Fatal("expected ErrGoalAlreadyRunning from concurrent RunSpec")
	}
}
