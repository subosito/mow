# contextsink

Per-session side channel for oversized tool results: store the body beside the session, replace live history with a short stub, recover later with `context_search`.

## Link

```go
import _ "github.com/subosito/mow/packs/contextsink"
```

Stock `cmd/mow` and `packs/mowi/cmd/mowi` blank-import this package. Library embeds that omit the import keep results inline and have no search tool.

## Commands and tools

| Surface | Name |
|---|---|
| Tool | `context_search` |
| Hook | `PostTool` (store + stub) |

No CLI command and no slash commands. `context_search` is read-only and `Enabled` only when `extensions.contextsink.enabled` is true.

Recovery args: `pattern` (string or list), optional `max_results`, `context_lines`; or get-by-id: `id`, optional `offset`, `window`. Search is pinned to the engine’s own `SessionDir`+`SessionID`. Stored files live under `<sid>.tools/`.

Storage is bounded (64 files / 32 MiB total, 8 MiB per file) and pruned. Observability events (metadata only): `harness.contextsink.store`, `harness.contextsink.recover`.

## Config (`extensions.contextsink`)

Default is **off**. Decode errors leave defaults (a bad section must not break the run).

```yaml
extensions:
  contextsink:
    enabled: true           # required to activate; default off
    max_inline_bytes: 8000  # above this → store + stub (default 8000; capped at 8 MiB)
```

## Docs

- [docs/extensions.md](../../docs/extensions.md) — Context sink pack
- [docs/harness.md](../../docs/harness.md) — context archive and `context_search`
- [docs/architecture.md](../../docs/architecture.md)
