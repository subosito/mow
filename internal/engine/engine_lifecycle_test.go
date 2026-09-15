package engine_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/subosito/mow"
)

func lifecycleEngine(t *testing.T) *mow.Engine {
	t.Helper()
	e, err := mow.New(mow.Options{
		Workspace: t.TempDir(),
		Chat: func(context.Context, []mow.Message, []mow.ToolSpec) (mow.Message, error) {
			return mow.Message{Role: "assistant", Content: "ok"}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e.Close() })
	return e
}

func TestStaleUnsubscribeKeepsReplacementListener(t *testing.T) {
	e := lifecycleEngine(t)
	unsubscribeOld := e.AddOnEvent(func(mow.Event) {})
	got := 0
	e.SetOnEvent(func(mow.Event) { got++ })
	unsubscribeOld()
	e.Emit(mow.Event{Type: mow.EventRunStart})
	if got != 1 {
		t.Fatalf("replacement listener called %d times; stale unsubscribe removed it", got)
	}
}

func TestPromptAfterClose(t *testing.T) {
	e := lifecycleEngine(t)
	if err := e.Close(); err != nil {
		t.Fatal(err)
	}
	res, err := e.Prompt(t.Context(), "hello")
	if err == nil {
		t.Fatalf("closed engine still ran prompt: text=%q stop=%q", res.Text, res.StopReason)
	}
}

func TestCloseCancelsRunningAndQueuedPrompts(t *testing.T) {
	started := make(chan struct{})
	var calls atomic.Int64
	e, err := mow.New(mow.Options{
		Workspace: t.TempDir(),
		Chat: func(ctx context.Context, _ []mow.Message, _ []mow.ToolSpec) (mow.Message, error) {
			if calls.Add(1) == 1 {
				close(started)
			}
			<-ctx.Done()
			return mow.Message{}, ctx.Err()
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e.Close() })
	done := make(chan error, 2)
	go func() { _, err := e.Prompt(t.Context(), "first"); done <- err }()
	<-started
	go func() { _, err := e.Prompt(t.Context(), "queued"); done <- err }()
	if err := e.Close(); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		select {
		case err := <-done:
			if err == nil {
				t.Fatal("prompt completed after Close")
			}
		case <-time.After(2 * time.Second):
			t.Fatal("Close left a prompt running")
		}
	}
	if calls.Load() != 1 {
		t.Fatal("queued prompt reached provider after Close")
	}
}

func TestSameLengthEditSurvivesResume(t *testing.T) {
	opt := mow.Options{
		Workspace: t.TempDir(), SessionID: "review-rewind",
		Chat: func(context.Context, []mow.Message, []mow.ToolSpec) (mow.Message, error) {
			return mow.Message{Role: "assistant", Content: "ok"}, nil
		},
	}
	e, err := mow.NewHarness(opt)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	if _, err := e.Prompt(t.Context(), "one"); err != nil {
		t.Fatal(err)
	}
	if _, ok := e.Rewind(); !ok {
		t.Fatal("rewind failed")
	}
	if _, err := e.Prompt(t.Context(), "two"); err != nil {
		t.Fatal(err)
	}
	if err := e.Close(); err != nil {
		t.Fatal(err)
	}
	resumed, err := mow.NewHarness(opt)
	if err != nil {
		t.Fatal(err)
	}
	defer resumed.Close()
	for _, m := range resumed.Messages() {
		if m.Role == "user" {
			if m.Content != "two" {
				t.Fatalf("resumed user prompt=%q; same-length edit was not persisted", m.Content)
			}
			return
		}
	}
	t.Fatal("no resumed user message")
}

func TestCancelDeferredCatalog(t *testing.T) {
	t.Setenv("MOW_API_KEY", "test-key")
	started := make(chan struct{})
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		select {
		case <-release:
		case <-r.Context().Done():
		}
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	defer srv.Close()
	e, err := mow.New(mow.Options{Workspace: t.TempDir(), DeferLLM: true, BaseURL: srv.URL, Model: "gpt-5-mini"})
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	done := make(chan error, 1)
	go func() { _, err := e.Prompt(t.Context(), "hello"); done <- err }()
	select {
	case <-started:
	case err := <-done:
		close(release)
		t.Fatalf("prompt failed before catalog request: %v", err)
	case <-time.After(3 * time.Second):
		close(release)
		t.Fatal("catalog request did not start")
	}
	e.Cancel()
	select {
	case err := <-done:
		close(release)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancel error=%v", err)
		}
	case <-time.After(2 * time.Second):
		close(release)
		<-done
		t.Fatal("Cancel did not interrupt catalog request; prompt returned only after server was released")
	}
}

func TestCancelWhileWaitingForDeferredCatalog(t *testing.T) {
	t.Setenv("MOW_API_KEY", "test-key")
	started, release := make(chan struct{}), make(chan struct{})
	releaseCatalog := sync.OnceFunc(func() { close(release) })
	signalStarted := sync.OnceFunc(func() { close(started) })
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		signalStarted()
		select {
		case <-release:
		case <-r.Context().Done():
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"gpt-5-mini"}]}`))
	}))
	defer srv.Close()
	defer releaseCatalog()
	e, err := mow.New(mow.Options{Workspace: t.TempDir(), DeferLLM: true, BaseURL: srv.URL, Model: "gpt-5-mini"})
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	done := make(chan error, 1)
	go func() { _, err := e.ListModels(t.Context()); done <- err }()
	select {
	case <-started:
	case err := <-done:
		t.Fatalf("catalog initialization failed: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("catalog request did not start")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	if _, err := e.ListModels(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("waiting caller error=%v, want deadline exceeded", err)
	}
	releaseCatalog()
	if err := <-done; err != nil {
		t.Fatalf("cancelling waiter interrupted initializer: %v", err)
	}
}
