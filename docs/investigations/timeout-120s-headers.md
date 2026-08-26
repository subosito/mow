# First-byte wait (was: 120s header timeout)

Shipped. Streaming waits for response headers / first byte for
`llm.first_byte_timeout_sec` (default **300s**, hard failure, not retried).
Non-streaming attempts use `llm.call_timeout_sec` (default **120s**). Both are
host/user config only — see `internal/config/mow.yaml.example`.

The old hardcoded `ResponseHeaderTimeout: 120s` on the default stream client
was that 120s wall. Tune `first_byte_timeout_sec` if a gateway holds the
request longer before the first byte (reasoning models often do).

Still open (not blockers for rc): typed upstream-stall classification, and
structured `llm.attempt_failed` diagnostics. Loop hosts already observe silence
via `loop.model.wait` / `loop.model.active`.
