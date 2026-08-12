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
	started := make(chan struct{}, 1)
	eng, err := mow.New(mow.Options{
		NoSession: true,
		Chat: func(ctx context.Context, messages []mow.Message, tools []mow.ToolSpec) (mow.Message, error) {
			select {
			case started <- struct{}{}:
			default:
			}
			select {
			case <-block:
				return mow.Message{Role: "assistant", Content: "GOAL_DONE"}, nil
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
	// Only the lock winner reaches the model. Wait for it to hold the run lock
	// inside Chat, then require the loser to fail before we release the winner
	// (so the second cannot simply start after the first finishes).
	select {
	case <-started:
	case <-time.After(10 * time.Second):
		t.Fatal("timeout waiting for first RunSpec to reach Chat")
	}
	select {
	case err := <-errCh:
		if !errors.Is(err, goal.ErrGoalAlreadyRunning) {
			close(block)
			wg.Wait()
			t.Fatalf("expected ErrGoalAlreadyRunning from concurrent RunSpec, got %v", err)
		}
	case <-time.After(5 * time.Second):
		close(block)
		wg.Wait()
		t.Fatal("timeout waiting for concurrent RunSpec rejection")
	}
	close(block)
	wg.Wait()
	close(errCh)
	// Drain the winner's result (should not also be AlreadyRunning).
	for err := range errCh {
		if errors.Is(err, goal.ErrGoalAlreadyRunning) {
			t.Fatal("winner should not return ErrGoalAlreadyRunning")
		}
	}
}
