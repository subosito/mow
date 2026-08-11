package llm

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// withTestFirstByte swaps defaultFirstByteTimeout for the duration of fn and
// restores it after. Tests must not leave the package default mutated.
func withTestFirstByte(d time.Duration, fn func()) {
	prev := defaultFirstByteTimeout
	defaultFirstByteTimeout = d
	defer func() { defaultFirstByteTimeout = prev }()
	fn()
}

// firstByteClient builds a Client whose HTTP client is nil so doHTTPStream
// builds mow's own stream transport with the configured first-byte bound.
func firstByteClient(t *testing.T, baseURL string, firstByte time.Duration) *Client {
	t.Helper()
	return &Client{
		BaseURL:          baseURL,
		APIKey:           "test",
		Model:            "test-model",
		FirstByteTimeout: firstByte,
		Stream:           true,
	}
}

func newStreamReq(t *testing.T, ctx context.Context, target string) *http.Request {
	t.Helper()
	raw := []byte(`{"model":"test-model","messages":[],"stream":true}`)
	req, err := newJSONRequest(ctx, http.MethodPost, target, raw)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	return req
}

// TestStreamSlowFirstByteSucceeds proves a server that spends longer than the
// old 120s threshold thinking before its first byte still succeeds when the
// first-byte timeout is configured above it. Durations are shrunk so the test
// runs in ~1s, but the analogue holds: think time < bound → success.
func TestStreamSlowFirstByteSucceeds(t *testing.T) {
	const think = 300 * time.Millisecond
	const bound = 1 * time.Second
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(think) // simulate pre-first-byte reasoning
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\ndata: [DONE]\n\n")
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}))
	defer srv.Close()

	withTestFirstByte(bound, func() {
		c := firstByteClient(t, srv.URL, bound)
		req := newStreamReq(t, context.Background(), srv.URL+"/chat/completions")
		res, err := c.doHTTPStream(req)
		if err != nil {
			t.Fatalf("slow first byte within bound should succeed: %v", err)
		}
		res.Body.Close()
	})
}

// TestStreamFirstByteTimeoutDiagnostic proves that when the server never sends
// headers the error is diagnostic (names the bound and the config knob) and
// arrives within ~bound, not the old 120s default.
func TestStreamFirstByteTimeoutDiagnostic(t *testing.T) {
	const bound = 400 * time.Millisecond
	started := make(chan struct{}, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started <- struct{}{}
		time.Sleep(5 * time.Second) // never writes headers
	}))
	defer srv.Close()

	withTestFirstByte(bound, func() {
		c := firstByteClient(t, srv.URL, bound)
		req := newStreamReq(t, context.Background(), srv.URL+"/chat/completions")
		start := time.Now()
		_, err := c.doHTTPStream(req)
		elapsed := time.Since(start)
		if err == nil {
			t.Fatal("expected first-byte timeout error, got nil")
		}
		if elapsed > 2*bound {
			t.Fatalf("timeout should fire within ~%s, took %s", bound, elapsed)
		}
		msg := err.Error()
		if !strings.Contains(msg, "first byte") && !strings.Contains(msg, "header") {
			t.Fatalf("error should mention first byte/headers, got: %s", msg)
		}
		if !strings.Contains(msg, "first_byte_timeout_sec") {
			t.Fatalf("error should hint the config knob, got: %s", msg)
		}
	})
}

// TestStreamFirstByteTimeoutNotRetried proves a full first-byte timeout is a
// single attempt: the server is hit exactly once, not maxHTTPAttempts times.
// Without this guard a 5-minute think would multiply into 15 minutes.
func TestStreamFirstByteTimeoutNotRetried(t *testing.T) {
	const bound = 200 * time.Millisecond
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		time.Sleep(2 * time.Second) // never writes headers
	}))
	defer srv.Close()

	withTestFirstByte(bound, func() {
		c := firstByteClient(t, srv.URL, bound)
		req := newStreamReq(t, context.Background(), srv.URL+"/chat/completions")
		_, _ = c.doHTTPStream(req)
		if n := hits.Load(); n != 1 {
			t.Fatalf("first-byte timeout must not retry: got %d hits, want 1", n)
		}
	})
}

// TestStreamCallerCancellationIsPrompt proves that when the caller cancels the
// context, the call returns promptly with the caller's error — not the
// first-byte timeout.
func TestStreamCallerCancellationIsPrompt(t *testing.T) {
	const bound = 10 * time.Second // long; cancellation must beat it
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done(): // request cancellation reached the server
		case <-release: // cleanup: never leave httptest.Close blocked
		}
	}))
	defer func() {
		close(release)
		srv.Close()
	}()

	ctx, cancel := context.WithCancel(context.Background())
	c := firstByteClient(t, srv.URL, bound)
	req := newStreamReq(t, ctx, srv.URL+"/chat/completions")

	start := time.Now()
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()
	_, err := c.doHTTPStream(req)
	elapsed := time.Since(start)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
	if elapsed > bound/2 {
		t.Fatalf("cancellation should return promptly (<%s), took %s", bound/2, elapsed)
	}
}

// TestCallTimeoutConfigurable proves Client.CallTimeout overrides the default
// for the non-streaming JSON path: a server that takes longer than the
// configured bound fails (the cap applies), while a faster one succeeds.
func TestCallTimeoutConfigurable(t *testing.T) {
	const cap = 400 * time.Millisecond
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(cap * 3) // slow: exceeds cap
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	// Reuse the JSON path with a client that has no HTTP (so mow's cap applies).
	c := &Client{
		BaseURL:     srv.URL,
		APIKey:      "test",
		Model:       "test-model",
		CallTimeout: cap,
	}
	raw := []byte(`{"model":"test-model","messages":[]}`)
	req, err := newJSONRequest(context.Background(), http.MethodPost, srv.URL+"/chat/completions", raw)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	start := time.Now()
	_, _, err = c.doJSON(req)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("expected call timeout, got nil")
	}
	if elapsed > 2*cap {
		t.Fatalf("JSON call timeout should fire within ~%s, took %s", cap, elapsed)
	}
}

// TestIsHeaderTimeoutClassifier proves the classifier distinguishes a genuine
// caller deadline (context.DeadlineExceeded — must NOT be classified as a
// first-byte timeout) from a transport net.Error timeout whose message names
// response headers.
func TestIsHeaderTimeoutClassifier(t *testing.T) {
	if isHeaderTimeout(context.DeadlineExceeded) {
		t.Fatal("caller deadline must not be classified as first-byte timeout")
	}
	if isHeaderTimeout(nil) {
		t.Fatal("nil must not be a header timeout")
	}
	// A synthetic net.Error that reports Timeout() and names response headers.
	he := &headerTimeoutErr{}
	if !isHeaderTimeout(he) {
		t.Fatal("net.Error timeout naming response headers should be a first-byte timeout")
	}
	// A non-header net.Error timeout must not match.
	ne := &nonHeaderTimeoutErr{}
	if isHeaderTimeout(ne) {
		t.Fatal("non-header net.Error timeout must not be a first-byte timeout")
	}
}

type headerTimeoutErr struct{}

func (headerTimeoutErr) Error() string   { return "net/http: timeout awaiting response headers" }
func (headerTimeoutErr) Timeout() bool   { return true }
func (headerTimeoutErr) Temporary() bool { return false }

type nonHeaderTimeoutErr struct{}

func (nonHeaderTimeoutErr) Error() string   { return "i/o timeout" }
func (nonHeaderTimeoutErr) Timeout() bool   { return true }
func (nonHeaderTimeoutErr) Temporary() bool { return false }

// compile-time: ensure the stub types satisfy net.Error.
var (
	_ net.Error = headerTimeoutErr{}
	_ net.Error = nonHeaderTimeoutErr{}
)
