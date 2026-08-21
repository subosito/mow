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

// RetryKind classifies a scheduled retry so hosts can render honest copy
// instead of a generic "gateway silent" wait.
type RetryKind int

const (
	// RetryKindBusy: the gateway answered a retryable status (429/5xx, or an
	// overload error dressed as HTTP 200) — alive but overloaded.
	RetryKindBusy RetryKind = iota
	// RetryKindUnavailable: connection refused/reset at connect time — the
	// gateway is down or restarting.
	RetryKindUnavailable
	// RetryKindNetwork: any other transient transport error.
	RetryKindNetwork
)

// RetryInfo describes one scheduled retry of an LLM HTTP call. It carries no
// URL, headers, or credentials — safe to surface to hosts as-is.
type RetryInfo struct {
	// Attempt is the 1-based ordinal of the upcoming retry (first retry = 1).
	Attempt int
	// Delay is the backoff sleep before the next attempt starts.
	Delay time.Duration
	// Status is the retryable HTTP status (429/5xx), or 0 for
	// transport-level failures and in-body overload signals.
	Status int
	Kind   RetryKind
}

// notifyRetry fires the OnRetry callback once per scheduled retry, just
// after the backoff is decided and before the sleep.
func notifyRetry(fn func(RetryInfo), info RetryInfo) {
	if fn != nil {
		fn(info)
	}
}

// maxConnRefusedAttempts is the extra budget for connection-refused errors
// (upstream down/restarting): a gateway bounce can take tens of seconds, and a
// run must survive it instead of dying at the ~1.4s generic burst. Once the
// budget is spent the original error is returned (a permanently-dead server
// should not stall the caller forever). Vars so tests can shrink them.
var maxConnRefusedAttempts = 12

// connRefusedBaseDelay is the base backoff for connection-refused retries
// (doubles twice then caps); tests shrink it.
var connRefusedBaseDelay = time.Second

// serverRestarting reports whether err is the local gateway bouncing:
// nothing listening (ECONNREFUSED), peer reset during drain, or a
// connect-time EOF. These deserve a much longer retry window than generic
// transients. http.Client wraps the net error in *url.Error, so we match
// both errors.Is and the usual dial strings.
func serverRestarting(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.ECONNRESET) {
		return true
	}
	s := strings.ToLower(err.Error())
	if strings.Contains(s, "connection refused") {
		return true
	}
	if strings.Contains(s, "connection reset by peer") {
		return true
	}
	// Connect-time close while systemd is restarting the listener.
	if strings.Contains(s, "connect:") && strings.Contains(s, "connection reset") {
		return true
	}
	return false
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

// defaultFirstByteTimeout bounds how long a streaming call waits for the
// first response byte/headers (300s). It matches streamIdleTimeout so "no
// bytes for X" means the same X before and after the first byte: a
// long-reasoning model that spends minutes thinking before its first SSE
// chunk gets the same grace as inter-chunk silence. Vars so tests can shrink
// them.
var defaultFirstByteTimeout = 5 * time.Minute

// defaultCallTimeout bounds a single non-streaming call (one attempt, not the
// whole retry sequence). The streaming path has idleReader; the JSON path had
// nothing, so a host that injects &http.Client{Timeout: 0} — or any very large
// timeout — could hang a run forever on a wedged gateway. Var so tests can
// shrink it.
var defaultCallTimeout = 120 * time.Second

func (c *Client) firstByteTimeout() time.Duration {
	if c.FirstByteTimeout > 0 {
		return c.FirstByteTimeout
	}
	return defaultFirstByteTimeout
}

func (c *Client) callTimeout() time.Duration {
	if c.CallTimeout > 0 {
		return c.CallTimeout
	}
	return defaultCallTimeout
}

// doHTTPStream retries a replayable request using a long-lived client when
// c.HTTP is nil.
// Timeout is 0 for the overall body (streams can run for minutes) but dial and
// response-header waits are bounded so a silent gateway cannot freeze forever.
// The response-header (first-byte) bound is c.FirstByteTimeout (default
// defaultFirstByteTimeout); a full first-byte timeout is a hard, non-retried
// failure — it does not multiply across attempts.
func (c *Client) doHTTPStream(req *http.Request) (*http.Response, error) {
	hc := c.HTTP
	if hc == nil {
		hc = streamHTTPClient(c.firstByteTimeout())
	}
	return doWithRetry(hc, req, maxHTTPAttempts, c.OnRetry)
}

// streamHTTPClient: no overall Timeout (SSE can be long); bound connect +
// headers. firstByte sets ResponseHeaderTimeout (the pre-first-byte bound);
// it defaults to defaultFirstByteTimeout via doHTTPStream.
func streamHTTPClient(firstByte time.Duration) *http.Client {
	if firstByte <= 0 {
		firstByte = defaultFirstByteTimeout
	}
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
			ResponseHeaderTimeout: firstByte, // wait for first byte/headers
			ExpectContinueTimeout: 1 * time.Second,
		},
	}
}

func doWithRetry(hc *http.Client, req *http.Request, attempts int, onRetry func(RetryInfo)) (*http.Response, error) {
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
			// ResponseHeaderTimeout is a complete first-byte wait, not a short
			// transient attempt. Return one actionable diagnostic instead of
			// multiplying a five-minute bound across generic retries.
			if responseHeaderTimedOut(err) {
				return nil, fmt.Errorf("llm: timed out after %s waiting for response headers/first byte (configure llm.first_byte_timeout_sec to allow more pre-first-byte think time): %w", headerTimeoutBound(hc), err)
			}
			if !retryableNetErr(err) {
				return nil, err
			}
			// A first-byte timeout (ResponseHeaderTimeout) is a hard,
			// non-retried failure: the model spent the whole budget thinking
			// before emitting a byte, and retrying would just wait the same
			// span again — multiplying a 5-minute think into 15. Return a
			// diagnostic error that names the bound and the configured
			// duration without leaking the request URL or credentials.
			if isHeaderTimeout(err) {
				return nil, fmt.Errorf("llm: timed out after %s waiting for response headers/first byte (configure llm.first_byte_timeout_sec to allow more pre-first-byte think time)", headerTimeoutBound(hc))
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
				notifyRetry(onRetry, RetryInfo{Attempt: refused, Delay: wait, Kind: RetryKindUnavailable})
				continue
			}
			if i == attempts-1 {
				return nil, err
			}
			wait = retryDelay(i+1, nil)
			notifyRetry(onRetry, RetryInfo{Attempt: i + 1, Delay: wait, Kind: RetryKindNetwork})
			continue
		}
		if retryableStatus(res.StatusCode) && i < attempts-1 {
			wait = retryDelay(i+1, res)
			notifyRetry(onRetry, RetryInfo{Attempt: i + 1, Delay: wait, Status: res.StatusCode, Kind: RetryKindBusy})
			_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 1<<20))
			res.Body.Close()
			continue
		}
		return res, nil
	}
}

// isHeaderTimeout reports whether err is a ResponseHeaderTimeout firing. On
// HTTP/1 the transport wraps it as a net.Error whose Timeout() is true; on
// HTTP/2 (x/net) it surfaces as a context deadline or a response-header
// timeout error string. We match conservatively so a genuine caller deadline
// (context.DeadlineExceeded) is NOT treated as a first-byte timeout — those
// are already classified as non-retryable by retryableNetErr.
func isHeaderTimeout(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		s := err.Error()
		if strings.Contains(s, "response header") || strings.Contains(s, "timeout awaiting response") {
			return true
		}
	}
	return false
}

// headerTimeoutBound returns the configured ResponseHeaderTimeout on the
// client's transport, or the default when it cannot be read.
func responseHeaderTimedOut(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) {
		return false
	}
	return strings.Contains(strings.ToLower(err.Error()), "timeout awaiting response headers")
}

func headerTimeoutBound(hc *http.Client) time.Duration {
	if hc != nil {
		if t, ok := hc.Transport.(*http.Transport); ok && t != nil {
			if t.ResponseHeaderTimeout > 0 {
				return t.ResponseHeaderTimeout
			}
		}
	}
	return defaultFirstByteTimeout
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
