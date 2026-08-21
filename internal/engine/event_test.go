package engine_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

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

func TestCompactEventPayload(t *testing.T) {
	raw, err := json.Marshal(mow.Event{Type: mow.EventCompact, Layer: mow.CompactLayerDrop,
		CharsBefore: 1000, CharsAfter: 400, CharsSaved: 600,
		MessagesBefore: 12, MessagesAfter: 5, OverBudget: true})
	if err != nil {
		t.Fatal(err)
	}
	got := string(raw)
	for _, want := range []string{`"type":"loop.compact"`, `"layer":"drop"`, `"chars_before":1000`,
		`"chars_after":400`, `"chars_saved":600`, `"messages_before":12`, `"messages_after":5`, `"over_budget":true`} {
		if !strings.Contains(got, want) {
			t.Fatalf("payload %s missing %s", got, want)
		}
	}
}

func TestCompactStartEventPayload(t *testing.T) {
	raw, err := json.Marshal(mow.Event{Type: mow.EventCompactStart, Auto: true})
	if err != nil {
		t.Fatal(err)
	}
	got := string(raw)
	for _, want := range []string{`"type":"loop.compact.start"`, `"auto":true`} {
		if !strings.Contains(got, want) {
			t.Fatalf("payload %s missing %s", got, want)
		}
	}
}

// Pre-first-byte activity: a blocked LLM call must produce loop.model.wait
// (request sent; waiting for response) while it sits silent, and exactly one
// loop.model.active when the call returns. The real threshold ticks are
// 10s/30s out, so a briefly held call sees no intermediate waits.
func TestPromptModelWaitActiveEvents(t *testing.T) {
	inChat := make(chan struct{})
	release := make(chan struct{})
	var mu sync.Mutex
	var evs []mow.Event
	eng, err := mow.New(mow.Options{
		NoSession: true,
		Model:     "gpt-5-mini",
		Chat: func(ctx context.Context, messages []mow.Message, tools []mow.ToolSpec) (mow.Message, error) {
			close(inChat)
			<-release
			return mow.Message{Role: "assistant", Content: "ok"}, nil
		},
		OnEvent: func(ev mow.Event) {
			if ev.Type == mow.EventModelWait || ev.Type == mow.EventModelActive {
				mu.Lock()
				evs = append(evs, ev)
				mu.Unlock()
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := eng.Prompt(context.Background(), "hi")
		done <- err
	}()
	<-inChat
	deadline := time.Now().Add(2 * time.Second)
	for {
		mu.Lock()
		n := len(evs)
		mu.Unlock()
		if n > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("no loop.model.wait event while the call was blocked")
		}
		time.Sleep(time.Millisecond)
	}
	mu.Lock()
	wait := evs[0]
	mu.Unlock()
	if wait.Type != mow.EventModelWait {
		t.Fatalf("first event=%q, want %q", wait.Type, mow.EventModelWait)
	}
	if wait.Delta != "request sent; waiting for response" {
		t.Fatalf("wait delta=%q", wait.Delta)
	}
	if wait.Model != "gpt-5-mini" {
		t.Fatalf("wait model=%q, want gpt-5-mini", wait.Model)
	}
	if wait.RunID == "" {
		t.Fatal("wait event missing run_id")
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(evs) != 2 || evs[1].Type != mow.EventModelActive {
		t.Fatalf("events=%v, want exactly wait then active", evs)
	}
	if evs[1].Model != "gpt-5-mini" {
		t.Fatalf("active model=%q, want gpt-5-mini", evs[1].Model)
	}
}

// Options.Chat never sees stream hooks, so wait ends only when Chat returns.
// The Provider seam is the production path: the first OnToken must emit
// loop.model.active while the call is still in flight.
type delayedTokenProvider struct {
	inChat  chan struct{}
	goToken chan struct{}
	release chan struct{}
}

func (p *delayedTokenProvider) Chat(_ context.Context, _ []mow.Message, _ []mow.ToolSpec, hooks mow.ChatHooks) (mow.Message, error) {
	close(p.inChat)
	<-p.goToken
	if hooks.OnToken != nil {
		hooks.OnToken("hi")
	}
	<-p.release
	return mow.Message{Role: "assistant", Content: "hi"}, nil
}

func TestPromptModelWaitActiveOnFirstToken(t *testing.T) {
	p := &delayedTokenProvider{
		inChat:  make(chan struct{}),
		goToken: make(chan struct{}),
		release: make(chan struct{}),
	}
	var mu sync.Mutex
	var evs []mow.Event
	eng, err := mow.New(mow.Options{
		NoSession: true,
		Model:     "gpt-5-mini",
		Provider:  p,
		OnEvent: func(ev mow.Event) {
			if ev.Type == mow.EventModelWait || ev.Type == mow.EventModelActive {
				mu.Lock()
				evs = append(evs, ev)
				mu.Unlock()
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := eng.Prompt(context.Background(), "hi")
		done <- err
	}()
	<-p.inChat
	waitForEvents(t, &mu, &evs, 1)
	close(p.goToken)
	waitForEvents(t, &mu, &evs, 2)
	mu.Lock()
	got := evs[1].Type
	mu.Unlock()
	if got != mow.EventModelActive {
		t.Fatalf("after first token: %q, want %q (active must not wait for Chat return)", got, mow.EventModelActive)
	}
	close(p.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

// Error before any upstream activity must not emit loop.model.active —
// "responding" would be a lie. The wait ticker stops regardless, and
// run.end clears the host's wait state.
func TestPromptModelWaitNoActiveOnError(t *testing.T) {
	var mu sync.Mutex
	var types []string
	eng, err := mow.New(mow.Options{
		NoSession: true,
		Model:     "gpt-5-mini",
		Chat: func(context.Context, []mow.Message, []mow.ToolSpec) (mow.Message, error) {
			return mow.Message{}, errors.New("upstream down")
		},
		OnEvent: func(ev mow.Event) {
			mu.Lock()
			types = append(types, string(ev.Type))
			mu.Unlock()
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = eng.Prompt(context.Background(), "hi")
	if err == nil {
		t.Fatal("expected chat error")
	}
	mu.Lock()
	defer mu.Unlock()
	joined := strings.Join(types, ",")
	if !strings.Contains(joined, string(mow.EventModelWait)) {
		t.Fatalf("missing wait on error path: %v", types)
	}
	if strings.Contains(joined, string(mow.EventModelActive)) {
		t.Fatalf("active on error-before-activity implies responding: %v", types)
	}
	waitAt, endAt := -1, -1
	for i, ty := range types {
		switch ty {
		case string(mow.EventModelWait):
			if waitAt < 0 {
				waitAt = i
			}
		case string(mow.EventRunEnd):
			endAt = i
		}
	}
	if waitAt < 0 || endAt < 0 || waitAt >= endAt {
		t.Fatalf("order wait < run.end (run.end clears the wait), got %v", types)
	}
}

// A provider whose reply streams frames but no content/reasoning deltas (a
// tool-call-only reply) still ends the wait via ChatHooks.OnActivity — the
// first upstream frame — not only when Chat returns.
type activityOnlyProvider struct {
	inChat  chan struct{}
	goFrame chan struct{}
	release chan struct{}
}

func (p *activityOnlyProvider) Chat(_ context.Context, _ []mow.Message, _ []mow.ToolSpec, hooks mow.ChatHooks) (mow.Message, error) {
	close(p.inChat)
	<-p.goFrame
	if hooks.OnActivity != nil {
		hooks.OnActivity()
	}
	<-p.release
	return mow.Message{Role: "assistant", Content: "done"}, nil
}

func TestPromptModelActiveOnUpstreamFrame(t *testing.T) {
	p := &activityOnlyProvider{
		inChat:  make(chan struct{}),
		goFrame: make(chan struct{}),
		release: make(chan struct{}),
	}
	var mu sync.Mutex
	var evs []mow.Event
	eng, err := mow.New(mow.Options{
		NoSession: true,
		Model:     "gpt-5-mini",
		Provider:  p,
		OnEvent: func(ev mow.Event) {
			if ev.Type == mow.EventModelWait || ev.Type == mow.EventModelActive {
				mu.Lock()
				evs = append(evs, ev)
				mu.Unlock()
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := eng.Prompt(context.Background(), "hi")
		done <- err
	}()
	<-p.inChat
	waitForEvents(t, &mu, &evs, 1) // wait (request sent)
	close(p.goFrame)               // first upstream frame; no content delta
	waitForEvents(t, &mu, &evs, 2)
	mu.Lock()
	got := evs[1].Type
	mu.Unlock()
	if got != mow.EventModelActive {
		t.Fatalf("after upstream frame: %q, want %q (active must not wait for Chat return)", got, mow.EventModelActive)
	}
	close(p.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

// A retryable gateway answer (HTTP 429) surfaces as loop.model.retry with
// honest copy before the backoff sleep — the status code is the most it
// names, never the request URL — and the run then completes on the next
// attempt.
func TestPromptModelRetryEventOn429(t *testing.T) {
	var chatHits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" || r.URL.Path == "/models" {
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{{"id": "gpt-5-mini"}}})
			return
		}
		if chatHits.Add(1) == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"back\"}}]}\n\ndata: [DONE]\n\n")
	}))
	t.Cleanup(srv.Close)

	t.Setenv("MOW_API_KEY", "test-key")
	t.Setenv("OPENAI_API_KEY", "test-key")
	t.Setenv("ANTHROPIC_API_KEY", "")

	var mu sync.Mutex
	var evs []mow.Event
	eng, err := mow.New(mow.Options{
		NoSession: true,
		BaseURL:   srv.URL + "/v1",
		Model:     "gpt-5-mini",
		OnEvent: func(ev mow.Event) {
			mu.Lock()
			evs = append(evs, ev)
			mu.Unlock()
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = eng.Close() })

	res, err := eng.Prompt(context.Background(), "hi")
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "back" {
		t.Fatalf("text=%q want the second attempt's answer", res.Text)
	}
	mu.Lock()
	defer mu.Unlock()
	var retry *mow.Event
	tokenAt, retryAt := -1, -1
	for i, ev := range evs {
		switch ev.Type {
		case mow.EventModelRetry:
			if retry != nil {
				t.Fatal("exactly one retry was scheduled; got a second loop.model.retry")
			}
			e := ev
			retry = &e
			retryAt = i
		case "loop.token": // not re-exported in mow.go; the wire name is stable
			if tokenAt < 0 {
				tokenAt = i
			}
		}
	}
	if retry == nil {
		types := make([]string, 0, len(evs))
		for _, ev := range evs {
			types = append(types, string(ev.Type))
		}
		t.Fatalf("missing loop.model.retry on the 429 path: %v", types)
	}
	if !strings.Contains(retry.Delta, "provider busy") || !strings.Contains(retry.Delta, "retrying in") {
		t.Fatalf("retry copy %q must name the status and the backoff", retry.Delta)
	}
	if strings.Contains(retry.Delta, srv.URL) || strings.Contains(retry.Delta, "127.0.0.1") {
		t.Fatalf("retry copy %q leaks the request URL", retry.Delta)
	}
	if retryAt > tokenAt {
		t.Fatal("loop.model.retry must precede the first token (the retry happens before any content)")
	}
	if retry.Model != "gpt-5-mini" {
		t.Fatalf("retry event model=%q", retry.Model)
	}
}

func waitForEvents(t *testing.T, mu *sync.Mutex, evs *[]mow.Event, n int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		mu.Lock()
		got := len(*evs)
		mu.Unlock()
		if got >= n {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("have %d events, want %d", got, n)
		}
		time.Sleep(time.Millisecond)
	}
}
