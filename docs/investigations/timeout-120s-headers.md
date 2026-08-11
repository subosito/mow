# Investigation: model requests timing out at exactly 120s before response headers

Status: investigation report — no code changes made.

## Where the 120s comes from

`internal/llm/retry.go` — `streamHTTPClient()` (the default client used when a
host does not inject `Client.HTTP`):

```go
Timeout: 0,                                    // overall: unbounded (SSE can run minutes)
DialContext Timeout: 30s
TLSHandshakeTimeout:   15 * time.Second,
ResponseHeaderTimeout: 120 * time.Second,      // wait for first byte/headers  ← THE 120s
```

So "timeout at exactly 120s, before any headers" is Go's
`Transport.ResponseHeaderTimeout` firing: TCP connected, TLS done, request
sent, and the upstream (or a gateway in front of it) produced zero response
bytes for 120s. The error surface is the raw net/http one:
`net/http: request canceled while waiting for response (Client.Timeout or
context deadline exceeded)` / `context deadline exceeded`.

Adjacent timeouts (for comparison):

| Knob | Value | Where | Path |
|---|---|---|---|
| `ResponseHeaderTimeout` | **120s** (hardcoded) | `streamHTTPClient()` | openai-chat-completions SSE, openai-responses SSE, anthropic SSE |
| `streamIdleTimeout` | 5m (hardcoded) | `stream.go` `idleReader` | silence *after* headers on SSE body |
| `jsonCallTimeout` | 120s per attempt (hardcoded) | `jsonhttp.go` | non-streaming calls only |
| dial / TLS | 30s / 15s | transport | all |

## Retry classification today

`doWithRetry` (stream path):

- `maxHTTPAttempts = 3` for statuses 429/408/502/503/504/5xx and for
  `retryableNetErr` — which **does** include transport-level
  `context.DeadlineExceeded` (only ctx-driven cancel/deadline is excluded).
  So the 120s header timeout IS retried: **worst case ≈ 3×120s + ~1.4s
  backoff ≈ 361s of wall clock before the run dies**, silently, with no
  per-attempt diagnostics.
- Connection-refused gets a separate long budget (`maxConnRefusedAttempts=12`,
  ~40s window) to survive gateway restarts.
- Streaming request is replayable (`newJSONRequest` sets `GetBody`) and no
  bytes have been consumed before headers, so pre-header retry is safe.
  Post-header retry is correctly *not* attempted (would replay tokens).

`doJSON` additionally wraps each attempt in `context.WithTimeout(120s)`
(`capPerAttempt`) and treats that internal deadline as retryable
(`retryableAttemptErr`), so a host-injected `Timeout: 0` client can't hang a
JSON call forever.

## Gap analysis

1. **No config surface at all.** `internal/config` has zero LLM timeout/retry
   knobs (`bash_timeout_sec` exists for tools; nothing for the model call).
   `llm.system_prefix`, `llm.base_url`, etc. exist, but timeouts are
   compile-time constants. A user stuck behind a slow gateway cannot tune
   anything except injecting a custom `http.Client` (Go-only, host-level).
2. **120s is a one-size-fits-none constant.** Fine for OpenAI/Anthropic
   direct. Too short for: reasoning models with long prefill (o-series,
   R1-class), huge context on slow hardware, self-hosted vLLM/sglang behind a
   proxy, gateways that buffer before flushing headers. Too long for: a dead
   gateway that accepted TCP but will never answer (3 attempts × 120s).
3. **Header wait and body idle share no mental model.** 120s to first header,
   but 5m idle after first header. A gateway that sends headers immediately
   and then stalls gets 5 minutes; one that precomputes before flushing
   headers gets 120s. Inverted incentives.
4. **Diagnostics are poor.** The failure surfaces as a bare
   `context deadline exceeded` wrapped by net/http; `stopReasonFrom` in
   engine_prompt.go maps `context.DeadlineExceeded` → same bucket as user
   cancel-adjacent stops; nothing says "3 attempts × 120s header wait against
   <base_url>, model <model>, wire <wire>". Session JSONL records the failed
   run's `run.end` with stop_reason/error string, but no per-attempt detail,
   and there is no `llm.attempt` / `llm.error` event class on `OnEvent`.
5. **Mitigation vs. masking is conflated.** Conn-refused retry (40s window)
   is genuine restart-survival. Blindly raising `ResponseHeaderTimeout` or
   attempt count for a gateway that accepts TCP and never answers just turns
   a 2-minute failure into a 10-minute hang — same dead gateway, worse UX,
   and tool/agent turns pile up behind it.

## Recommendations (priority order)

### 1. Config knobs (config UX)

Add under `llm:` (defaults keep today's behavior, so nothing changes for
existing users):

```yaml
llm:
  timeout_header_sec: 120     # wait for response headers/first byte (0 = no bound)
  timeout_stream_idle_sec: 300  # silence after headers before failing SSE
  timeout_json_sec: 120       # per-attempt cap for non-streaming calls
  max_attempts: 3             # generic transient budget (429/5xx/net)
```

- Thread through `Engine` → `llm.Client` (fields next to `HTTP`); when
  `Client.HTTP` is host-injected, honor the host's client but still apply
  `timeout_header_sec` via a cloned transport only if non-zero — document
  precedence: explicit client wins, constants are the fallback.
- Keep the per-attempt `context.WithTimeout` cap pattern from `jsonhttp.go`
  for the stream header wait if we want host clients with `Timeout: 0`
  bounded; simplest correct route remains transport-level
  `ResponseHeaderTimeout` since it measures exactly the pre-header phase.
- CLI: no flag needed (config-only is fine, matches `session.dir` style), but
  mention in `mow.yaml.example` under the OSS-sample rules.

### 2. Retry classification changes

- **Classify pre-header deadline as its own category** (`ErrUpstreamStall`
  sentinel or a `*net.OpError`-style typed error) so:
  - it retries under the normal budget (as today),
  - `stopReasonFrom` can map it to a distinct `stop_reason` like
    `"upstream_timeout"` instead of the generic deadline bucket,
  - the error string includes `attempts=3`, `header_wait=120s`, `url`,
    `wire`, `model`.
- **Keep, do not extend, the generic 3-attempt budget for stalls.** Stalls
  are the weakest signal of transience (vs. 429/503). If a user raises
  `timeout_header_sec` for a known-slow gateway, they should usually *lower*
  `max_attempts` to 1–2 in the same breath — document this pairing.
- Do NOT retry post-header stream failures (already correct); the fix there
  is the idle timeout, not retries.
- Honor `Retry-After` already implemented — no change.

### 3. Diagnostics / session persistence

- Emit `llm.attempt_failed` (or reuse `tool.end`-style events) via `OnEvent`
  per failed attempt: `attempt`, `phase` (`connect|tls|headers|body_idle`),
  `elapsed_ms`, `status`, `err`. Cheap, and TUIs can render "retrying 2/3".
- In `run.end`, when the stop is an upstream stall, put the structured detail
  into the error string so session JSONL persists it (`sess.Append` already
  records the failing run's end event; the content is currently just the bare
  net/http message).
- Log at Warn (not Debug) with `base_url` on the *final* failure only —
  no secret material involved, headers are never logged today and should
  stay that way (Authorization is in headers).

### 4. Mitigation vs. masking — the actual guidance

- **Real gateway issue (slow prefill, buffering proxy):** raise
  `timeout_header_sec` (e.g. 300 for reasoning models on slow infra). This
  is legitimate because the request *would have succeeded* given time; the
  retry layer then rarely matters. Evidence it's this: occasional 120s
  timeouts but retries or resends succeed; gateway logs show long prefill.
- **Dead/wedged gateway:** raising timeouts masks it. The right signal is
  the *distribution*: conn-refused = restarting (already handled with the
  40s window); connect-then-silence repeated across attempts = wedged.
  Recommended behavior: after N consecutive pre-header stalls across a short
  window, fail fast on subsequent calls with an explicit "upstream not
  responding" error rather than waiting another full budget (circuit-breaker
  lite). This is optional/phase-2; the config knob + classification above is
  phase 1.
- Do not add automatic provider failover on stall — that turns a slow
  gateway into silent billing/model drift; failover should be an explicit
  host decision (mow is single-endpoint by design).

### 5. Defaults

Keep 120s / 5m / 3 attempts as defaults: they match current behavior and are
sane for public providers. The fix is configurability + classification +
diagnostics, not new defaults. If anything, consider lowering default
`max_attempts` to 2 for the stall class only in a future release once the
classification exists — a wedged upstream rarely heals in 600ms of backoff.

## Files touched by a future implementation (reference only)

- `internal/llm/retry.go` — parameterize `streamHTTPClient`, stall sentinel
- `internal/llm/jsonhttp.go` — parameterize `jsonCallTimeout`
- `internal/llm/stream.go` (+ `responses.go` idleReader use) — idle timeout param
- `internal/llm/client` construction (`openai.go`/`anthropic.go`) — new Client fields
- `internal/config/config.go` + `mow.yaml.example` — `llm.timeout_*` keys
- `internal/engine/engine*.go` — wire config into Client, stop_reason mapping, events
