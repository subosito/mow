# rpc

JSON-lines control plane for embedders: one JSON object per stdin line; responses and events on stdout.

## Link

```go
import _ "github.com/subosito/mow/ext/rpc"
```

Stock `cmd/mow` blank-imports this package. The Rust `mowi` sibling project
launches `mow rpc` as its TUI backend.

## Commands and tools

| Surface | Name |
|---|---|
| CLI | `rpc` (`mow rpc`) |

No tools and no slash commands. `mow rpc` always `Close`s the Engine on exit.

Methods (requests may omit `"jsonrpc":"2.0"`):

| Method | Notes |
|---|---|
| `prompt` | `params {text, ephemeral?}`; jail-safe `@path` refs are attached for the model; result includes `text`, `session_id`, `run_id`, `stop_reason`, `usage`, `ephemeral`, `attached[]` |
| `cancel` | abort the in-flight prompt |
| `status` | `busy`, `allow_write`, `allow_shell`, `ask_mode`, `pending_perm`, session/model fields, and security-scoped `extra_roots` metadata |
| `session` | alias `session_id`; includes the same `extra_roots` metadata |
| `sessions` | stored sessions for this project: `{sessions:[{id, updated, preview}]}` |
| `transcript` | user/assistant turns for resume: `{messages:[{role, content}]}` (each content capped at 32k runes) |
| `steer` | `params.text` injected into the running turn; empty text is an invalid request |
| `slash.list` | `{commands:[{name, summary, exclusive, aliases}]}` — only the packs linked into this binary; `usage` is omitted (fetch it with `slash` + `help`) |
| `slash` | `params {name, args[], color}`; result `{title, body}` and optional `error` for user-level Run failures (bad flags). `args:["help"]` returns usage without running. An `exclusive` command is refused while a turn is in flight |
| `perm.set` | `params.mode` `"ask"` or `"auto"` |
| `model.list` | `{models:[{id, current, wire?}], current}` |
| `model.set` | `params {id}` → `{ok, model}` |
| `context` | `{tokens, context_window?, remaining?, percent?}` |
| `compact` | `params {max_chars?}` → compaction report |
| `rewind` | `{ok, last_user}` — drop last exchange, refill input |
| `skill.list` | `{skills:[name]}` |
| `skill.activate` | `params {names[]}` → `{activated, unknown}` |
| `effort.list` | `{efforts:[{id,current}], current, default}` |
| `effort.set` | `params {id}` → `{ok, effort}` |
| `perm.decide` | `params {id, decision}` where decision is `allow`, `deny` or `always` |
| `capabilities` | `{rpc, methods[], control_methods[], features{}, optional?:{features[]}}` — feature-detect here |
| `version` | `rpc` is `"4"`; clients should accept `>=` their minimum |
| `ping` | |

Everything except `prompt` and `slash` is a control method: it is answered on a
dedicated channel, so `cancel`, `status` or `perm.decide` stay responsive while
a prompt runs.

`status` and `session` expose `extra_roots` as `{path, read_only}` rows, plus
the configured counts `extra_roots_rw` and `extra_roots_ro`. The primary
workspace is not counted. These are security metadata only; no Git or repository
presentation data is included.

`capabilities.optional.features` is present only when linked optional packages
register host-facing facilities. Each entry is
`{id, linked:true, events?:string[]}`. The list is dynamic and does not assume
the stock binary's imports. `slash.list` remains the source of truth for
optional slash commands; they are intentionally not duplicated in this
catalog. A linked feature may still require configuration before it produces
events (for example, LSP).

## Permission gating

By default the server is fail-open: tools run without asking, exactly as
before. A UI that wants confirmation sends `perm.set {"mode":"ask"}`. After
that, `write`, `edit` and `bash` calls emit a notification and block until the
UI answers:

```json
{"jsonrpc":"2.0","method":"perm.ask","params":{"id":"perm-1","name":"write","args":{…},"tool_call_id":"call-1"}}
{"id":9,"method":"perm.decide","params":{"id":"perm-1","decision":"allow"}}
```

`always` allows and stops asking for that tool for the rest of the session;
`deny` returns a denial as the tool result, so the turn still completes. Read
tools (`read`, `glob`, `grep`) never ask. Cancelling the turn while a decision
is outstanding aborts the run.

During prompt, unsolicited `{"method":"event","params":{…}}` lines may appear (`loop.token`, `harness.tool.start`, …). Event deltas and prompt text are size-capped (8k runes / 512k runes). Max stdin line is 1 MiB.

## Config

None. There is no `extensions.rpc` section. Engine flags are the same as `mow run`.

## Docs

- [docs/extensions.md](../../docs/extensions.md)
- [docs/harness.md](../../docs/harness.md)
- [docs/architecture.md](../../docs/architecture.md)
