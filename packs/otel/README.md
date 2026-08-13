# otel

Optional OTLP/HTTP tracing and metrics. Nested module so OpenTelemetry/grpc/protobuf dependencies do not enter a library-only embed.

## Link

```go
import _ "github.com/subosito/mow/packs/otel"
```

Stock `cmd/mow` and `packs/mowi/cmd/mowi` blank-import this package. The import registers `mow.SetOTELAuto`; `Engine.New` attaches the exporter when an endpoint is set. Empty endpoint means no exporter.

## Commands and tools

None. No CLI, no slash commands, no tools.

## Config (`otel:` — not `extensions.otel`)

Host/user config only (stripped from project `.mow/config`). A non-empty `endpoint` is on; set `enabled: false` to force off despite the endpoint.

```yaml
otel:
  enabled: true                     # optional; endpoint alone enables when omitted
  endpoint: http://127.0.0.1:4318   # empty = off
  protocol: http                    # http (default); grpc is reserved / not supported yet
  service_name: mow
  headers:
    authorization: Bearer …
```

Env (wins over file): `MOW_OTEL_ENDPOINT` / `OTEL_EXPORTER_OTLP_ENDPOINT`, `MOW_OTEL_PROTOCOL` / `OTEL_EXPORTER_OTLP_PROTOCOL`, `MOW_OTEL_SERVICE_NAME` / `OTEL_SERVICE_NAME`.

`Shutdown` is idempotent (auto-wire cleanup uses a 5s timeout). Span error/status text is redacted and length-capped. URL userinfo becomes an `Authorization` header when none is set.

When contextsink is also linked and configured, this pack exports `mow.contextsink.stored_results`, `mow.contextsink.saved_bytes`, and `mow.contextsink.recovered_bytes` counters.

## Docs

- [docs/extensions.md](../../docs/extensions.md) — OpenTelemetry
- [docs/harness.md](../../docs/harness.md) — OTEL export
- [docs/architecture.md](../../docs/architecture.md)
- [docs/embedding.md](../../docs/embedding.md) — programmatic `StartExport`
