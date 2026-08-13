package engine_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/subosito/mow"
)

// Auto-downshift is request-scoped: a short "thanks" may send medium on the
// wire, but Engine.Effort() (session/user setting, header chrome) stays high.
func TestSimplePromptDoesNotMutatePublicEffort(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var mu sync.Mutex
	var startEffort, startModel string
	eng, err := mow.New(mow.Options{
		NoSession: true,
		Model:     "gpt-5-mini",
		Effort:    "high",
		Chat: func(ctx context.Context, messages []mow.Message, tools []mow.ToolSpec) (mow.Message, error) {
			close(started)
			<-release
			return mow.Message{Role: "assistant", Content: "ok"}, nil
		},
		OnEvent: func(ev mow.Event) {
			if ev.Type == mow.EventRunStart {
				mu.Lock()
				startEffort = ev.Effort
				startModel = ev.Model
				mu.Unlock()
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = eng.Close() })

	if err := eng.SetEffort("high"); err != nil {
		t.Fatal(err)
	}

	errCh := make(chan error, 1)
	go func() {
		_, err := eng.Prompt(context.Background(), "thanks")
		errCh <- err
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for chat")
	}
	var releaseOnce sync.Once
	releaseNow := func() { releaseOnce.Do(func() { close(release) }) }
	defer releaseNow()
	if got := eng.Effort(); got != "high" {
		t.Fatalf("during simple prompt Effort()=%q want high", got)
	}
	mu.Lock()
	gotEffort, gotModel := startEffort, startModel
	mu.Unlock()
	if gotEffort != "medium" {
		t.Fatalf("run.start Effort=%q want medium (request downshift)", gotEffort)
	}
	if gotModel == "" {
		t.Fatal("run.start missing Model")
	}
	releaseNow()
	if err := <-errCh; err != nil {
		t.Fatal(err)
	}
	if got := eng.Effort(); got != "high" {
		t.Fatalf("after simple prompt Effort()=%q want high", got)
	}
}

func TestAutoEffortDownshiftIsOnTheWireOnly(t *testing.T) {
	var mu sync.Mutex
	var wireEffort string
	started := make(chan struct{})
	release := make(chan struct{})

	var startOnce sync.Once
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/models"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": []map[string]any{{
					"id":             "gpt-5-mini",
					"facet":          "chat",
					"efforts":        []string{"low", "medium", "high"},
					"default_effort": "medium",
				}},
			})
		case strings.HasSuffix(r.URL.Path, "/chat/completions"):
			body, _ := io.ReadAll(r.Body)
			var payload map[string]any
			_ = json.Unmarshal(body, &payload)
			mu.Lock()
			if e, _ := payload["reasoning_effort"].(string); e != "" {
				wireEffort = e
			}
			mu.Unlock()
			startOnce.Do(func() { close(started) })
			<-release
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\n")
			_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
			_, _ = io.WriteString(w, "data: [DONE]\n\n")
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	t.Setenv("MOW_HOME", t.TempDir())
	t.Setenv("MOW_API_KEY", "test-key")
	t.Setenv("OPENAI_API_KEY", "test-key")
	t.Setenv("ANTHROPIC_API_KEY", "")

	eng, err := mow.New(mow.Options{
		NoSession:      true,
		BaseURL:        srv.URL + "/v1",
		Model:          "gpt-5-mini",
		Effort:         "high",
		ExplicitEffort: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = eng.Close() })

	if got := eng.Effort(); got != "high" {
		t.Fatalf("before prompt Effort()=%q want high", got)
	}

	errCh := make(chan error, 1)
	go func() {
		_, err := eng.Prompt(context.Background(), "thanks")
		errCh <- err
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for chat request")
	}
	var releaseOnce sync.Once
	releaseNow := func() { releaseOnce.Do(func() { close(release) }) }
	defer releaseNow()
	if got := eng.Effort(); got != "high" {
		t.Fatalf("during wire call Effort()=%q want high", got)
	}
	mu.Lock()
	gotWire := wireEffort
	mu.Unlock()
	if gotWire != "medium" {
		t.Fatalf("reasoning_effort=%q want medium (request downshift)", gotWire)
	}
	releaseNow()
	if err := <-errCh; err != nil {
		t.Fatal(err)
	}
	if got := eng.Effort(); got != "high" {
		t.Fatalf("after prompt Effort()=%q want high", got)
	}
}
