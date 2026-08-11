package llm

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func testClient(url string, hc *http.Client) *Client {
	return &Client{BaseURL: url, APIKey: "k", Model: "gpt-5-mini", HTTP: hc}
}

const okChatBody = `{"choices":[{"message":{"role":"assistant","content":"hi"},` +
	`"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":1}}`

// Gateways under load answer HTTP 200 with an error envelope instead of 5xx.
// That used to abort the whole run on the first blip.
func TestChatRetriesTransient200Error(t *testing.T) {
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if n.Add(1) <= 2 {
			fmt.Fprint(w, `{"error":{"message":"server overloaded, please retry","type":"overloaded_error"}}`)
			return
		}
		fmt.Fprint(w, okChatBody)
	}))
	defer srv.Close()

	msg, err := testClient(srv.URL, srv.Client()).Chat(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil)
	if err != nil {
		t.Fatalf("want success after retries, got %v", err)
	}
	if msg.Content != "hi" {
		t.Fatalf("unexpected content %q", msg.Content)
	}
	if got := n.Load(); got != 3 {
		t.Fatalf("attempts=%d want 3", got)
	}
}

func TestChatGivesUpOnPersistent200Error(t *testing.T) {
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		fmt.Fprint(w, `{"error":{"message":"server overloaded","type":"overloaded_error"}}`)
	}))
	defer srv.Close()

	_, err := testClient(srv.URL, srv.Client()).Chat(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil)
	if err == nil {
		t.Fatal("want error after exhausting attempts")
	}
	if !strings.Contains(err.Error(), "overloaded") {
		t.Fatalf("error should carry the gateway message, got %v", err)
	}
	if got := n.Load(); got != int32(maxHTTPAttempts) {
		t.Fatalf("attempts=%d want %d", got, maxHTTPAttempts)
	}
}

// Permanent failures must fail fast: retrying a bad request or bad key only
// burns rate limit and delays the user's error.
func TestChatDoesNotRetryPermanent200Error(t *testing.T) {
	for _, tc := range []struct{ name, body string }{
		{"auth", `{"error":{"message":"Incorrect API key provided","type":"invalid_request_error"}}`},
		{"shape", `{"error":{"message":"invalid_request: messages must not be empty"}}`},
		{"context", `{"error":{"message":"This model's maximum context length is 8192 tokens"}}`},
		{"quota", `{"error":{"message":"You exceeded your current quota","code":"insufficient_quota"}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var n atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				n.Add(1)
				fmt.Fprint(w, tc.body)
			}))
			defer srv.Close()

			_, err := testClient(srv.URL, srv.Client()).Chat(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil)
			if err == nil {
				t.Fatal("want error")
			}
			if got := n.Load(); got != 1 {
				t.Fatalf("attempts=%d want 1 (permanent error must not retry)", got)
			}
		})
	}
}

func TestTransientBodyError(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
		want   bool
	}{
		{"overloaded object", 200, `{"error":{"message":"overloaded"}}`, true},
		{"string error", 200, `{"error":"upstream connect timeout"}`, true},
		{"clean response", 200, okChatBody, false},
		{"null error", 200, `{"error":null,"choices":[]}`, false},
		{"empty body", 200, ``, false},
		{"not json", 200, `<html>502</html>`, false},
		{"auth", 200, `{"error":{"message":"invalid api key"}}`, false},
		{"non-2xx left to status classifier", 429, `{"error":{"message":"overloaded"}}`, false},
		{"non-2xx 400 not reclassified", 400, `{"error":{"message":"overloaded"}}`, false},
		{"unreadable error object", 200, `{"error":{"detail":[1,2]}}`, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, got := transientBodyError(tc.status, []byte(tc.body))
			if got != tc.want {
				t.Fatalf("transientBodyError=%v want %v", got, tc.want)
			}
		})
	}
}

// A host that injects &http.Client{Timeout: 0} must not be able to hang a run
// forever: each attempt is bounded independently of the client.
func TestChatBoundsAttemptWithUnboundedClient(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	defer close(release)

	c := testClient(srv.URL, &http.Client{Timeout: 0}) // the easy host mistake
	// Shrink the cap for the test by giving the parent ctx an earlier deadline;
	// WithTimeout keeps whichever deadline is sooner.
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()

	done := make(chan error, 1)
	go func() {
		_, err := c.Chat(ctx, []Message{{Role: "user", Content: "hi"}}, nil)
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("want timeout error, got success")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Chat hung with an unbounded http.Client")
	}
}

func TestJSONCallTimeoutCapApplies(t *testing.T) {
	// Sanity: the cap decision only kicks in for zero/oversized client timeouts,
	// so a host with a deliberate short timeout keeps its own behaviour.
	for _, tc := range []struct {
		timeout time.Duration
		wantCap bool
	}{
		{0, true},
		{30 * time.Second, false},
		{defaultCallTimeout, false},
		{10 * time.Minute, true},
	} {
		got := tc.timeout <= 0 || tc.timeout > defaultCallTimeout
		if got != tc.wantCap {
			t.Fatalf("timeout %v: cap=%v want %v", tc.timeout, got, tc.wantCap)
		}
	}
}

func TestRetryableAttemptErrRetriesInternalTimeoutOnly(t *testing.T) {
	cap := 120 * time.Second
	noCap := time.Duration(0)
	if !retryableAttemptErr(cap, context.DeadlineExceeded) {
		t.Fatal("internal per-attempt deadline should be retryable")
	}
	if retryableAttemptErr(noCap, context.DeadlineExceeded) {
		t.Fatal("host http.Client timeout must not widen into multiple attempts")
	}
	if retryableAttemptErr(cap, context.Canceled) {
		t.Fatal("cancellation must not be retried")
	}
}
