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
| `transcript` | user/assistant turns for resume: `{messages:[{role, content, ts?}]}` (each content capped at 32k runes) |
| `steer` | `params.text` injected into the running turn; empty text is an invalid request |
| `slash.list` | `{commands:[{name, summary, exclusive, aliases}]}` — only the packs linked into this binary; `usage` is omitted (fetch it with `slash` + `help`) |
| `slash` | `params {name, args[], color}`; result `{title, body}` and optional `error` for user-level Run failures (bad flags). `args:["help"]` returns usage without running. An `exclusive` command is refused while a turn is in flight |
| `perm.set` | `params.mode` `"ask"` or `"auto"` |
| `model.list` | `{models:[{id, current, wire?}], current}` |
| `model.set` | `params {id}` → `{ok, model}` |
| `context` | `{tokens, context_window?, remaining?, percent?}` |
| `compact` | `params {max_chars?}` → compaction report |
| `rewind` | `{ok, last_user}` — drop last exchange, refill input |
| `skill.list` | `{skills:[folder], items?:[{id,name,folder,description?}]}` |
| `skill.activate` | `params {names[]}` → `{activated, unknown}` |
| `plugin.list` | `{plugins:[id], items?:[{id,name,version,description?,skills?}]}` |
| `effort.list` | `{efforts:[{id,current}], current, default}` |
| `effort.set` | `params {id}` → `{ok, effort}` |
| `config.list` | additive catalog `{items:[{id,name,type,current,set,options?}]}` for perm/model/effort; typed `*.set` stay the mutators |
| `perm.decide` | `params {id, decision}` where decision is `allow`, `deny` or `always` |
| `capabilities` | `{rpc, methods[], control_methods[], features{}, optional?:{features[]}}` — feature-detect here |
| `version` | `rpc` is `"1"` (compatibility epoch); clients require exact epoch match, then feature-detect |
| `ping` | |

Worker-queue methods are `prompt` and `compact` (depth 4; may return
"request queue full"). Everything else is control-routed on a dedicated
channel so `cancel`, `status` or `perm.decide` stay responsive while a prompt
runs. `slash` is control-routed but still executes in a goroutine.

`status` and `session` expose `extra_roots` as `{path, read_only}` rows, plus
the configured counts `extra_roots_rw` and `extra_roots_ro`. The primary
workspace is not counted. These are security metadata only; no Git or repository
presentation data is included.

`status` also includes `procs`: `{id, pid, alive, log}` rows from the workspace
`$MOW_HOME/proc/<hash>` store (same as `proc_start` / `mow proc`). The list is
empty when none are recorded.

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
{"jsonrpc":"2.0","method":"perm.ask","params":{"id":"perm-1","name":"write","args":{…},"tool_call_id":"call-1","title":"write note.txt","kind":"write","subject":{"kind":"tool_call","tool":"write","tool_call_id":"call-1"}}}
{"id":9,"method":"perm.decide","params":{"id":"perm-1","decision":"allow"}}
```

`title`, `kind`, and `subject` are additive (feature `perm_subject`). Older
hosts can keep reading `id` / `name` / `args`. `always` allows and stops
asking for that tool for the rest of the session; `deny` returns a denial as
the tool result, so the turn still completes. Read tools (`read`, `glob`,
`grep`) never ask. Cancelling the turn while a decision is outstanding
aborts the run.

During prompt, unsolicited `{"method":"event","params":{…}}` lines may appear
(`loop.token`, `harness.tool.start`, …). When `features.typed_updates` is
true, a parallel `{"method":"update","params":{"kind":"token|thought|tool|state|usage",…}}`
notification is also emitted. `event` stays the source of truth. Event
deltas and prompt text are size-capped (8k runes / 512k runes). Max stdin
line is 1 MiB.

## Config

None. There is no `extensions.rpc` section. Engine flags are the same as `mow run`.

## Docs

- [docs/extensions.md](../../docs/extensions.md)
- [docs/harness.md](../../docs/harness.md)
- [docs/architecture.md](../../docs/architecture.md)

## Compatibility

`version.rpc` is the method-surface compatibility epoch (currently `"1"`),
separate from both JSON-RPC `2.0` and the mow release in `version.version`.
Clients require an exact epoch match, then feature-detect additive methods
from `methods` / `control_methods` / `features`; they should not require a
particular mow release. Pre-release numbers 2–4 were never published epochs.
