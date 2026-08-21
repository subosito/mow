package llm

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"syscall"
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
	res, err := doWithRetry(srv.Client(), req, 3, nil)
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
	res, err := doWithRetry(srv.Client(), req, 3, nil)
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

func TestRetryableNetErr(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"context canceled", context.Canceled, false},
		{"wrapped canceled", fmt.Errorf("do: %w", context.Canceled), false},
		{"deadline exceeded", context.DeadlineExceeded, false},
		{"dns not found", &net.DNSError{Err: "no such host", IsNotFound: true}, false},
		{"dns not found in url.Error", &url.Error{Op: "Post", URL: "https://x", Err: &net.DNSError{Err: "no such host", IsNotFound: true}}, false},
		{"dns timeout", &net.DNSError{Err: "i/o timeout", IsTimeout: true}, true},
		{"unknown authority", x509.UnknownAuthorityError{}, false},
		{"cert invalid", x509.CertificateInvalidError{Cert: &x509.Certificate{}, Reason: x509.Expired}, false},
		{"hostname mismatch", x509.HostnameError{Certificate: &x509.Certificate{}, Host: "x"}, false},
		{"tls verification", &tls.CertificateVerificationError{Err: x509.UnknownAuthorityError{}}, false},
		{"connection refused", &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")}, true},
		{"url wrapped refused", &url.Error{Op: "Post", URL: "http://127.0.0.1:9420/v1/responses", Err: &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")}}, true},
		{"generic transient", errors.New("unexpected EOF"), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := retryableNetErr(tc.err); got != tc.want {
				t.Fatalf("retryableNetErr(%v)=%v want %v", tc.err, got, tc.want)
			}
		})
	}
}

func TestRetryDelayRetryAfterDate(t *testing.T) {
	// Date form must behave like the seconds form: honour near-future dates,
	// cap far-future dates at 30s (not fall through to the 200ms base).
	cases := []struct {
		name     string
		after    time.Duration
		min, max time.Duration
	}{
		{"near future", 5 * time.Second, 2 * time.Second, 5 * time.Second},
		{"far future capped", 10 * time.Minute, 25 * time.Second, 30 * time.Second},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := &http.Response{Header: http.Header{}}
			res.Header.Set("Retry-After", time.Now().Add(tc.after).UTC().Format(http.TimeFormat))
			d := retryDelay(1, res)
			if d < tc.min || d > tc.max {
				t.Fatalf("delay=%v want between %v and %v", d, tc.min, tc.max)
			}
		})
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

func TestRetryDelayJitterBounds(t *testing.T) {
	// Backoff must stay in [base, 1.25*base) so concurrent runs that hit the
	// same 429 do not wake in lockstep.
	for attempt := 1; attempt <= 4; attempt++ {
		base := 200 * time.Millisecond * time.Duration(1<<(attempt-1))
		if base > 5*time.Second {
			base = 5 * time.Second
		}
		var distinct = map[time.Duration]bool{}
		for i := 0; i < 200; i++ {
			d := retryDelay(attempt, nil)
			if d < base || d >= base+base/4+time.Millisecond {
				t.Fatalf("attempt %d: delay %v out of [%v,%v)", attempt, d, base, base+base/4)
			}
			distinct[d] = true
		}
		if len(distinct) < 2 {
			t.Fatalf("attempt %d: no jitter observed", attempt)
		}
	}
}

func TestRetryDelayHonorsRetryAfter(t *testing.T) {
	res := &http.Response{Header: http.Header{}}
	res.Header.Set("Retry-After", "2")
	if got := retryDelay(1, res); got != 2*time.Second {
		t.Fatalf("Retry-After ignored: %v", got)
	}
	// Server-directed waits are used verbatim (no jitter), but capped.
	res.Header.Set("Retry-After", "600")
	if got := retryDelay(1, res); got != 30*time.Second {
		t.Fatalf("Retry-After not capped: %v", got)
	}
}

// ---- connection-refused survival (upstream restart) ----

// refusedRoundTripper always fails with ECONNREFUSED (server down/restarting).
type refusedRoundTripper struct{ n int }

func (r *refusedRoundTripper) RoundTrip(*http.Request) (*http.Response, error) {
	r.n++
	return nil, &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED}
}

// flakyRoundTripper fails with a generic retryable net error.
type flakyRoundTripper struct {
	n   int
	err error
}

func (r *flakyRoundTripper) RoundTrip(*http.Request) (*http.Response, error) {
	r.n++
	return nil, r.err
}

func shrinkRefusedBudget(t *testing.T) {
	t.Helper()
	oldN, oldD := maxConnRefusedAttempts, connRefusedBaseDelay
	maxConnRefusedAttempts = 3
	connRefusedBaseDelay = time.Millisecond
	t.Cleanup(func() {
		maxConnRefusedAttempts, connRefusedBaseDelay = oldN, oldD
	})
}

func TestDoWithRetrySurvivesConnectionRefused(t *testing.T) {
	shrinkRefusedBudget(t)
	rt := &refusedRoundTripper{}
	hc := &http.Client{Transport: rt}
	req, _ := http.NewRequest(http.MethodGet, "http://127.0.0.1:1/", nil)
	_, err := doWithRetry(hc, req, maxHTTPAttempts, nil)
	if err == nil {
		t.Fatal("expected connection-refused error")
	}
	if !errors.Is(err, syscall.ECONNREFUSED) {
		t.Fatalf("expected ECONNREFUSED, got %v", err)
	}
	// The refused budget extends past the generic burst (3) — a pure-refused
	// stream burns the refused budget + 1 final failing attempt.
	if got := rt.n; got != maxConnRefusedAttempts+1 || got <= maxHTTPAttempts {
		t.Fatalf("connection refused retry window = %d attempts, want %d (generic was %d)", got, maxConnRefusedAttempts+1, maxHTTPAttempts)
	}
}

func TestDoJSONSurvivesConnectionRefused(t *testing.T) {
	shrinkRefusedBudget(t)
	rt := &refusedRoundTripper{}
	c := &Client{HTTP: &http.Client{Transport: rt}}
	req, _ := http.NewRequest(http.MethodGet, "http://127.0.0.1:1/", nil)
	_, _, err := c.doJSON(req)
	if err == nil {
		t.Fatal("expected connection-refused error")
	}
	if !errors.Is(err, syscall.ECONNREFUSED) {
		t.Fatalf("expected ECONNREFUSED, got %v", err)
	}
	if got := rt.n; got != maxConnRefusedAttempts+1 || got <= maxHTTPAttempts {
		t.Fatalf("doJSON refused retry window = %d attempts, want %d", got, maxConnRefusedAttempts+1)
	}
}

func TestDoWithRetryGenericBurstUnchanged(t *testing.T) {
	rt := &flakyRoundTripper{err: &net.OpError{Op: "dial", Net: "tcp", Err: io.EOF}}
	hc := &http.Client{Transport: rt}
	req, _ := http.NewRequest(http.MethodGet, "http://127.0.0.1:1/", nil)
	_, err := doWithRetry(hc, req, maxHTTPAttempts, nil)
	if err == nil {
		t.Fatal("expected error")
	}
	if rt.n != maxHTTPAttempts {
		t.Fatalf("generic burst = %d attempts, want %d (unchanged)", rt.n, maxHTTPAttempts)
	}
}

func TestServerRestartingAndRefusedDelay(t *testing.T) {
	ref := &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED}
	if !serverRestarting(ref) {
		t.Fatal("ECONNREFUSED must classify as server restarting")
	}
	wrapped := &url.Error{Op: "Post", URL: "http://127.0.0.1:9420/v1/responses", Err: ref}
	if !serverRestarting(wrapped) {
		t.Fatal("url.Error-wrapped ECONNREFUSED must classify as restarting")
	}
	textOnly := fmt.Errorf(`Post "http://127.0.0.1:9420/v1/responses": dial tcp 127.0.0.1:9420: connect: connection refused`)
	if !serverRestarting(textOnly) {
		t.Fatal("string-wrapped connection refused must classify as restarting")
	}
	reset := &url.Error{Op: "Post", URL: "http://127.0.0.1:9420/v1/responses", Err: syscall.ECONNRESET}
	if !serverRestarting(reset) {
		t.Fatal("ECONNRESET must classify as restarting")
	}
	if serverRestarting(io.EOF) || serverRestarting(nil) {
		t.Fatal("non-refused errors must not classify as restarting")
	}
	// Delay grows 1x, 2x, 4x, then caps at 4x.
	base := connRefusedBaseDelay
	if retryDelayRefused(1) < base || retryDelayRefused(2) < 2*base ||
		retryDelayRefused(3) < 3*base || retryDelayRefused(10) < 3*base {
		t.Fatal("refused delay must grow then cap")
	}
}

func TestDoWithRetrySurvivesWrappedRefusedThenOK(t *testing.T) {
	shrinkRefusedBudget(t)
	var n atomic.Int32
	hc := &http.Client{Transport: RoundTripFunc(func(*http.Request) (*http.Response, error) {
		if n.Add(1) < 3 {
			return nil, &url.Error{
				Op:  "Post",
				URL: "http://127.0.0.1:9420/v1/responses",
				Err: &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED},
			}
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(http.NoBody),
			Header:     make(http.Header),
		}, nil
	})}
	req, _ := http.NewRequest(http.MethodPost, "http://127.0.0.1:9420/v1/responses", nil)
	res, err := doWithRetry(hc, req, maxHTTPAttempts, nil)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if n.Load() != 3 {
		t.Fatalf("attempts=%d want 3", n.Load())
	}
}

type RoundTripFunc func(*http.Request) (*http.Response, error)

func (f RoundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// ---- retry observer (OnRetry) ----
//
// RetryInfo has no string fields at all: no err.Error(), no URL, no headers.
// Leakage is therefore structural — these tests pin the classification
// contract (Kind/Status/Attempt/Delay) hosts render copy from.

func TestDoWithRetryNotifiesClassifiedBusy(t *testing.T) {
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if n.Add(1) == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	req, err := newJSONRequest(context.Background(), http.MethodPost, srv.URL, []byte(`{"x":1}`))
	if err != nil {
		t.Fatal(err)
	}
	var got []RetryInfo
	res, err := doWithRetry(srv.Client(), req, 3, func(ri RetryInfo) { got = append(got, ri) })
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if len(got) != 1 {
		t.Fatalf("notifications=%d want exactly 1 (one per scheduled retry)", len(got))
	}
	ri := got[0]
	if ri.Attempt != 1 || ri.Status != http.StatusTooManyRequests || ri.Kind != RetryKindBusy {
		t.Fatalf("RetryInfo=%+v want {Attempt:1 Status:429 Kind:RetryKindBusy}", ri)
	}
	if ri.Delay <= 0 {
		t.Fatalf("Delay=%v want > 0 backoff", ri.Delay)
	}
}

func TestDoWithRetryNotifiesClassifiedRefused(t *testing.T) {
	shrinkRefusedBudget(t)
	rt := &refusedRoundTripper{}
	hc := &http.Client{Transport: rt}
	req, _ := http.NewRequest(http.MethodGet, "http://127.0.0.1:1/", nil)
	var got []RetryInfo
	_, err := doWithRetry(hc, req, maxHTTPAttempts, func(ri RetryInfo) { got = append(got, ri) })
	if err == nil {
		t.Fatal("expected connection-refused error")
	}
	// One notification per scheduled retry; the final failing attempt
	// schedules nothing, so it does not notify.
	if len(got) != maxConnRefusedAttempts {
		t.Fatalf("notifications=%d want %d", len(got), maxConnRefusedAttempts)
	}
	for i, ri := range got {
		if ri.Kind != RetryKindUnavailable || ri.Status != 0 || ri.Attempt != i+1 {
			t.Fatalf("notification %d = %+v want {Attempt:%d Status:0 Kind:RetryKindUnavailable}", i, ri, i+1)
		}
		if ri.Delay <= 0 {
			t.Fatalf("notification %d Delay=%v want > 0", i, ri.Delay)
		}
	}
}

func TestDoWithRetryNotifiesClassifiedNetwork(t *testing.T) {
	rt := &flakyRoundTripper{err: &net.OpError{Op: "dial", Net: "tcp", Err: io.EOF}}
	hc := &http.Client{Transport: rt}
	req, _ := http.NewRequest(http.MethodGet, "http://127.0.0.1:1/", nil)
	var got []RetryInfo
	_, err := doWithRetry(hc, req, maxHTTPAttempts, func(ri RetryInfo) { got = append(got, ri) })
	if err == nil {
		t.Fatal("expected error")
	}
	if len(got) != maxHTTPAttempts-1 {
		t.Fatalf("notifications=%d want %d", len(got), maxHTTPAttempts-1)
	}
	for i, ri := range got {
		if ri.Kind != RetryKindNetwork || ri.Status != 0 || ri.Attempt != i+1 {
			t.Fatalf("notification %d = %+v want {Attempt:%d Status:0 Kind:RetryKindNetwork}", i, ri, i+1)
		}
	}
}

func TestDoJSONNotifiesInBodyOverload(t *testing.T) {
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if n.Add(1) == 1 {
			// Overload dressed as success: HTTP 200 with an error body.
			_, _ = w.Write([]byte(`{"error":{"message":"overloaded","type":"server_error"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	c := &Client{HTTP: srv.Client()}
	var got []RetryInfo
	c.OnRetry = func(ri RetryInfo) { got = append(got, ri) }
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/chat/completions", nil)
	status, body, err := c.doJSON(req)
	if err != nil {
		t.Fatal(err)
	}
	if status != http.StatusOK || !strings.Contains(string(body), "ok") {
		t.Fatalf("status=%d body=%q", status, body)
	}
	if len(got) != 1 {
		t.Fatalf("notifications=%d want 1", len(got))
	}
	// An in-body overload carries no HTTP status — the body was 200.
	if got[0].Kind != RetryKindBusy || got[0].Status != 0 || got[0].Attempt != 1 {
		t.Fatalf("RetryInfo=%+v want {Attempt:1 Status:0 Kind:RetryKindBusy}", got[0])
	}
}
