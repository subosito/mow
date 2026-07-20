package llm

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestDoWithRetrySucceedsAfter5xx(t *testing.T) {
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if n.Add(1) < 2 {
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte("nope"))
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`ok`))
	}))
	defer srv.Close()

	raw := []byte(`{"x":1}`)
	req, err := newJSONRequest(context.Background(), http.MethodPost, srv.URL, raw)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := doWithRetry(srv.Client(), req, 3)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	if res.StatusCode != 200 || string(b) != "ok" {
		t.Fatalf("status=%d body=%q", res.StatusCode, b)
	}
	if n.Load() != 2 {
		t.Fatalf("attempts=%d want 2", n.Load())
	}
}

func TestDoWithRetryHonoursRetryAfter(t *testing.T) {
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if n.Add(1) == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	req, err := newJSONRequest(context.Background(), http.MethodGet, srv.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	res, err := doWithRetry(srv.Client(), req, 3)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if time.Since(start) > 2*time.Second {
		t.Fatalf("retry waited too long")
	}
	if n.Load() != 2 {
		t.Fatalf("attempts=%d", n.Load())
	}
}

func TestRetryableStatus(t *testing.T) {
	if !retryableStatus(429) || !retryableStatus(503) {
		t.Fatal("expected 429/503 retryable")
	}
	if retryableStatus(400) || retryableStatus(200) {
		t.Fatal("400/200 should not retry")
	}
}
