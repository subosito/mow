package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// jsonBodyLimit caps how much of a non-streaming response we buffer.
const jsonBodyLimit = 8 << 20

// doJSON performs a non-streaming LLM call with retries and returns the status
// and buffered body. It is the JSON-path counterpart of doHTTPStream: never use
// it for SSE (a retry mid-stream would replay tokens the caller already saw).
//
// Beyond transport/status retries it also retries a *semantic* failure: many
// gateways signal overload with HTTP 200 and {"error":{...}} in the body. Those
// used to abort the whole run on the first blip.
func (c *Client) doJSON(req *http.Request) (int, []byte, error) {
	// callTimeout is the per-attempt cap. A caller-supplied HTTP client
	// (c.HTTP) keeps its own semantics: we only cap per attempt when using
	// mow's own default client (hc.Timeout <= 0) or when the caller's timeout
	// exceeds the configured bound. A caller with a tighter Timeout wins.
	perAttempt := c.callTimeout()
	hc := c.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: perAttempt}
	}
	// Bound each attempt regardless of the client's own Timeout. WithTimeout
	// keeps the parent's deadline when that one is earlier, so a caller with a
	// tighter ctx still wins. capPerAttempt is the per-attempt bound (0 = do
	// not add an internal cap, e.g. a caller-supplied HTTP client with its own
	// Timeout already in force).
	capPerAttempt := time.Duration(0)
	if hc.Timeout <= 0 || hc.Timeout > perAttempt {
		capPerAttempt = perAttempt
	}

	var (
		lastErr    error
		lastStatus int
		lastBody   []byte
		wait       time.Duration
		refused    int
	)
	// Connection-refused (upstream down/restarting) gets a much longer window
	// than the generic burst, mirroring doWithRetry: a gateway bounce can take
	// tens of seconds and a run must survive it. Generic transients keep the
	// short maxHTTPAttempts budget.
	// Bound the loop so the retry tail below stays reachable: the conn-refused
	// window is the only path that may exceed maxHTTPAttempts, and it is
	// capped separately by maxConnRefusedAttempts.
	for attempt := 1; attempt <= maxHTTPAttempts+maxConnRefusedAttempts; attempt++ {
		if err := req.Context().Err(); err != nil {
			return 0, nil, err
		}
		if attempt > 1 {
			if err := rewindRequest(req); err != nil {
				return 0, nil, err
			}
			if wait <= 0 {
				wait = retryDelay(attempt-1, nil)
			}
			if err := sleepCtx(req.Context(), wait); err != nil {
				return 0, nil, err
			}
			wait = 0
		}

		status, body, hdr, err := doJSONAttempt(hc, req, capPerAttempt)
		if err != nil {
			lastErr = err
			if req.Context().Err() != nil {
				return 0, nil, req.Context().Err()
			}
			if !retryableAttemptErr(capPerAttempt, err) {
				return 0, nil, err
			}
			if serverRestarting(err) {
				refused++
				if refused > maxConnRefusedAttempts {
					return 0, nil, err
				}
				wait = retryDelayRefused(refused)
				continue
			}
			if attempt == maxHTTPAttempts {
				return 0, nil, err
			}
			wait = retryDelay(attempt, nil)
			continue
		}
		lastStatus, lastBody = status, body

		if attempt < maxHTTPAttempts {
			if retryableStatus(status) {
				lastErr = fmt.Errorf("llm: HTTP %d", status)
				wait = retryDelay(attempt, &http.Response{Header: hdr})
				continue
			}
			// HTTP 200 + {"error": ...}: overload dressed as success.
			if msg, ok := transientBodyError(status, body); ok {
				lastErr = fmt.Errorf("llm: %s", msg)
				wait = retryDelay(attempt, &http.Response{Header: hdr})
				continue
			}
		}
		return status, body, nil
	}
	if lastStatus != 0 {
		return lastStatus, lastBody, nil
	}
	if lastErr != nil {
		return 0, nil, lastErr
	}
	return 0, nil, fmt.Errorf("llm: request failed after %d attempts", maxHTTPAttempts)
}

// retryableAttemptErr distinguishes an internal per-attempt timeout from the
// caller cancelling or exhausting the whole run. The former is an upstream
// stall and should consume the normal jittered retry budget; the latter must
// return immediately. net/http often wraps both as context deadline exceeded.
// internalCap is the per-attempt bound; 0 means no internal cap was applied
// (a caller-supplied HTTP client's own Timeout is in force, so a deadline
// is the caller's, not ours — do not retry).
func retryableAttemptErr(internalCap time.Duration, err error) bool {
	if err == nil {
		return false
	}
	// Only the timeout introduced by doJSONAttempt is retryable here. A host's
	// explicit http.Client.Timeout remains its requested whole-call bound.
	if errors.Is(err, context.DeadlineExceeded) {
		return internalCap > 0
	}
	return retryableNetErr(err)
}

// doJSONAttempt runs one request and fully buffers the body so the connection
// is released before any backoff sleep.
func doJSONAttempt(hc *http.Client, req *http.Request, capPerAttempt time.Duration) (int, []byte, http.Header, error) {
	r := req
	if capPerAttempt > 0 {
		ctx, cancel := context.WithTimeout(req.Context(), capPerAttempt)
		defer cancel()
		r = req.Clone(ctx)
	}
	res, err := hc.Do(r)
	if err != nil {
		return 0, nil, nil, err
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, jsonBodyLimit))
	if err != nil {
		return 0, nil, nil, err
	}
	return res.StatusCode, body, res.Header, nil
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// bodyError is the error envelope shared by OpenAI, Anthropic and most
// compatible gateways. "error" is an object on OpenAI/Anthropic and a bare
// string on some proxies, so it is decoded loosely.
type bodyError struct {
	Error json.RawMessage `json:"error"`
}

type bodyErrorObj struct {
	Message string `json:"message"`
	Type    string `json:"type"`
	Code    string `json:"code"`
}

// permanentErrorMarkers are substrings that mean a retry cannot help: the
// request itself is wrong (shape, auth, model, quota, policy).
var permanentErrorMarkers = []string{
	"invalid_request",
	"invalid request",
	"authentication",
	"unauthorized",
	"invalid api key",
	"invalid_api_key",
	"permission",
	"forbidden",
	"not_found",
	"not found",
	"model_not_found",
	"context_length",
	"context length",
	"maximum context",
	"too many tokens",
	"content_filter",
	"content_policy",
	"insufficient_quota",
	"billing",
	"unsupported",
}

// transientBodyError reports whether a 2xx body actually carries an error that
// is worth retrying. Only 2xx is inspected: real error statuses are already
// classified by retryableStatus, and re-deciding them here could retry a 400.
func transientBodyError(status int, body []byte) (string, bool) {
	if status < 200 || status >= 300 || len(body) == 0 {
		return "", false
	}
	var env bodyError
	if err := json.Unmarshal(body, &env); err != nil || len(env.Error) == 0 {
		return "", false
	}
	if string(env.Error) == "null" {
		return "", false
	}
	var msg string
	var obj bodyErrorObj
	if err := json.Unmarshal(env.Error, &obj); err == nil {
		msg = strings.TrimSpace(strings.Join([]string{obj.Message, obj.Type, obj.Code}, " "))
	}
	if strings.TrimSpace(msg) == "" {
		var s string
		if err := json.Unmarshal(env.Error, &s); err == nil {
			msg = s
		}
	}
	if strings.TrimSpace(msg) == "" {
		// An error object we cannot read: not provably transient, and the
		// caller will surface it as a hard error anyway.
		return "", false
	}
	low := strings.ToLower(msg)
	for _, m := range permanentErrorMarkers {
		if strings.Contains(low, m) {
			return "", false
		}
	}
	return strings.TrimSpace(msg), true
}
