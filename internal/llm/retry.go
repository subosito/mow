package llm

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"math"
	"math/rand/v2"
	"net"
	"net/http"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// maxHTTPAttempts for transient failures (429 / 5xx / some network errors).
const maxHTTPAttempts = 3

// maxConnRefusedAttempts is the extra budget for connection-refused errors
// (upstream down/restarting): a gateway bounce can take tens of seconds, and a
// run must survive it instead of dying at the ~1.4s generic burst. Once the
// budget is spent the original error is returned (a permanently-dead server
// should not stall the caller forever). Vars so tests can shrink them.
var maxConnRefusedAttempts = 12

// connRefusedBaseDelay is the base backoff for connection-refused retries
// (doubles twice then caps); tests shrink it.
var connRefusedBaseDelay = time.Second

// serverRestarting reports whether err is a connection refusal — the peer
// accepted the TCP handshake or nothing is listening (server down/restarting).
// These deserve a much longer retry window than generic transients.
func serverRestarting(err error) bool {
	if err == nil {
		return false
	}
	return errors.Is(err, syscall.ECONNREFUSED)
}

// retryDelayRefused grows the backoff for connection-refused retries:
// 1s, 2s, 4s, then 4s cap, so the full budget spans ~40s of restart window
// with light jitter to avoid synchronized reconnect storms.
func retryDelayRefused(n int) time.Duration {
	if n <= 0 {
		n = 1
	}
	d := connRefusedBaseDelay * time.Duration(1<<min(n-1, 2)) // 1x, 2x, 4x, 4x…
	if j := time.Duration(rand.Int64N(int64(d / 4))); j > 0 {
		d += j
	}
	return d
}

// doHTTPStream retries a replayable request using a long-lived client when
// c.HTTP is nil.
// Timeout is 0 for the overall body (streams can run for minutes) but dial and
// response-header waits are bounded so a silent gateway cannot freeze forever.
func (c *Client) doHTTPStream(req *http.Request) (*http.Response, error) {
	hc := c.HTTP
	if hc == nil {
		hc = streamHTTPClient()
	}
	return doWithRetry(hc, req, maxHTTPAttempts)
}

// streamHTTPClient: no overall Timeout (SSE can be long); bound connect + headers.
func streamHTTPClient() *http.Client {
	return &http.Client{
		Timeout: 0,
		Transport: &http.Transport{
			Proxy: http.ProxyFromEnvironment,
			DialContext: (&net.Dialer{
				Timeout:   30 * time.Second,
				KeepAlive: 30 * time.Second,
			}).DialContext,
			ForceAttemptHTTP2:     true,
			MaxIdleConns:          16,
			IdleConnTimeout:       90 * time.Second,
			TLSHandshakeTimeout:   15 * time.Second,
			ResponseHeaderTimeout: 120 * time.Second, // wait for first byte/headers
			ExpectContinueTimeout: 1 * time.Second,
		},
	}
}

func doWithRetry(hc *http.Client, req *http.Request, attempts int) (*http.Response, error) {
	if attempts < 1 {
		attempts = 1
	}
	var wait time.Duration
	refused := 0
	for i := 0; ; i++ {
		if err := req.Context().Err(); err != nil {
			return nil, err
		}
		if i > 0 {
			if err := rewindRequest(req); err != nil {
				return nil, err
			}
			if wait <= 0 {
				wait = retryDelay(i, nil)
			}
			t := time.NewTimer(wait)
			select {
			case <-req.Context().Done():
				t.Stop()
				return nil, req.Context().Err()
			case <-t.C:
			}
			wait = 0
		}
		res, err := hc.Do(req)
		if err != nil {
			if !retryableNetErr(err) {
				return nil, err
			}
			// Connection refused = upstream down/restarting: survive the bounce
			// with a much longer window than generic transients (the generic
			// burst is ~1.4s; a gateway restart can take tens of seconds).
			if serverRestarting(err) {
				refused++
				if refused > maxConnRefusedAttempts {
					return nil, err
				}
				wait = retryDelayRefused(refused)
				continue
			}
			if i == attempts-1 {
				return nil, err
			}
			wait = retryDelay(i+1, nil)
			continue
		}
		if retryableStatus(res.StatusCode) && i < attempts-1 {
			wait = retryDelay(i+1, res)
			_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 1<<20))
			res.Body.Close()
			continue
		}
		return res, nil
	}
}

func rewindRequest(req *http.Request) error {
	if req.Body == nil || req.Body == http.NoBody {
		return nil
	}
	if req.GetBody == nil {
		return fmt.Errorf("llm: cannot retry: request body not replayable")
	}
	body, err := req.GetBody()
	if err != nil {
		return err
	}
	req.Body = body
	return nil
}

func retryableStatus(code int) bool {
	return code == http.StatusTooManyRequests || code == http.StatusRequestTimeout ||
		code == http.StatusBadGateway || code == http.StatusServiceUnavailable ||
		code == http.StatusGatewayTimeout || code >= 500
}

func retryableNetErr(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	// Belt-and-braces for errors that stringify context cancellation without wrapping it.
	s := err.Error()
	if strings.Contains(s, "context canceled") || strings.Contains(s, "context deadline") {
		return false
	}
	// Permanent failures — retrying cannot help.
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) && dnsErr.IsNotFound {
		return false
	}
	var tlsVerify *tls.CertificateVerificationError
	var unknownCA x509.UnknownAuthorityError
	var certInvalid x509.CertificateInvalidError
	var hostname x509.HostnameError
	if errors.As(err, &tlsVerify) || errors.As(err, &unknownCA) ||
		errors.As(err, &certInvalid) || errors.As(err, &hostname) {
		return false
	}
	return true
}

func retryDelay(attempt int, res *http.Response) time.Duration {
	if res != nil {
		if ra := strings.TrimSpace(res.Header.Get("Retry-After")); ra != "" {
			if sec, err := strconv.Atoi(ra); err == nil && sec > 0 {
				d := time.Duration(sec) * time.Second
				if d > 30*time.Second {
					d = 30 * time.Second
				}
				return d
			}
			if t, err := http.ParseTime(ra); err == nil {
				if d := time.Until(t); d > 0 {
					if d > 30*time.Second {
						d = 30 * time.Second
					}
					return d
				}
			}
		}
	}
	// 200ms, 400ms, 800ms… plus jitter.
	if attempt < 1 {
		attempt = 1
	}
	base := 200 * time.Millisecond
	d := time.Duration(float64(base) * math.Pow(2, float64(attempt-1)))
	if d > 5*time.Second {
		d = 5 * time.Second
	}
	return withJitter(d)
}

// withJitter spreads retries over [d, 1.25d). Without it, every concurrent
// call that hit the same 429 wakes at the same instant and re-stampedes the
// provider — parallel agent runs share one rate limit.
func withJitter(d time.Duration) time.Duration {
	if d <= 0 {
		return d
	}
	return d + time.Duration(rand.Int64N(int64(d)/4+1))
}

// newJSONRequest builds a POST with replayable body for retries.
func newJSONRequest(ctx context.Context, method, url string, raw []byte) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, url, io.NopCloser(bytes.NewReader(raw)))
	if err != nil {
		return nil, err
	}
	req.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(raw)), nil
	}
	req.ContentLength = int64(len(raw))
	return req, nil
}
