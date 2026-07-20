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
