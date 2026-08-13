# rpc

JSON-lines control plane for embedders: one JSON object per stdin line; responses and events on stdout.

## Link

```go
import _ "github.com/subosito/mow/ext/rpc"
```

Stock `cmd/mow` and `packs/mowi/cmd/mowi` blank-import this package.

## Commands and tools

| Surface | Name |
|---|---|
| CLI | `rpc` (`mow rpc` / `mowi rpc`) |

No tools and no slash commands. `mow rpc` always `Close`s the Engine on exit.

Methods (requests may omit `"jsonrpc":"2.0"`):

| Method | Notes |
|---|---|
| `prompt` | `params.text`; result includes `text`, `session_id`, `run_id`, `stop_reason` |
| `cancel` | abort the in-flight prompt |
| `status` | concurrent with prompt (dedicated channel, not starved by a full queue) |
| `session` | alias `session_id` |
| `version` | |
| `ping` | |

During prompt, unsolicited `{"method":"event","params":{…}}` lines may appear (`loop.token`, `harness.tool.start`, …). Event deltas and prompt text are size-capped (8k runes / 512k runes). Max stdin line is 1 MiB.

## Config

None. There is no `extensions.rpc` section. Engine flags are the same as `mow run`.

## Docs

- [docs/extensions.md](../../docs/extensions.md)
- [docs/harness.md](../../docs/harness.md)
- [docs/architecture.md](../../docs/architecture.md)
