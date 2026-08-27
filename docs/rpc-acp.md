# `mow rpc` ↔ ACP coverage

How the native host protocol (`mow rpc`, epoch 1) maps onto Agent Client
Protocol v1 (stable) and v2 (draft).

**Direction:** ACP is the public host protocol (editors, desktop, third-party
clients). RPC is a first-party dialect for mowi until mowi is an ACP client.
Do not grow new product on RPC-only. Leftovers that ACP does not specify
(steer, compact, rewind, skills/plugins, extra-root ro/rw) belong as
**optional methods on the same `mow acp` connection**, capability-gated —
not a second process and not `_mow/*` as a parallel product.

Mowi should see ACP + those extras on one JSON-RPC session. Generic ACP
clients see only the standard.

Sources: [ACP v1 overview](https://agentclientprotocol.com/protocol/v1/overview),
[v2 overview](https://agentclientprotocol.com/protocol/v2/overview),
[v2 migration](https://agentclientprotocol.com/protocol/v2/migration),
[`ext/rpc`](../ext/rpc/README.md), [`ext/acp`](../ext/acp/README.md).

## Roles

| Surface | Process | Audience |
|---|---|---|
| `mow rpc` | first-party control plane | mowi and embedders |
| `mow acp` | ACP agent on stdio | Zed / JetBrains / other ACP clients |
| `acp_delegate` | mow as ACP *client* | named peers (`agents[]`, `mow_agents`) |

Do not put mowi on ACP as the primary wire. Custom `_mow/*` methods would
re-invent RPC inside ACP's envelope and still pay v1→v2 churn.

## Coverage

Status:

- **same** — both sides can do the job
- **shape** — same job, different envelope
- **rpc** — first-party only; keep on RPC
- **extra** — optional method on the same `mow acp` connection; RPC still has it
- **acp** — editor/interop only; do not copy into RPC
- **steal** — ACP shape is cleaner; fold into RPC additively (epoch 1)

| Job | `mow rpc` | ACP v1 | ACP v2 (draft) | Status |
|---|---|---|---|---|
| Handshake | `version` / `capabilities` (`rpc=1`) | `initialize` (`protocolVersion`) | same + richer caps | shape |
| New session | spawn flags + `session` | `session/new` | `session/new` + `cwd` / `additionalDirectories` | shape |
| Resume | `--session` / `--continue` + `transcript` | `session/load` (optional) | required list/resume/close | shape |
| List sessions | `sessions` | `session/list` (capability) | required | same |
| Close / delete | process exit | `session/close`, `session/delete` | required | shape |
| Prompt | `prompt` `{text, ephemeral?}` | `session/prompt` (blocks until stop) | ack now; `state_update` later | shape |
| Stream answer | `event` `loop.token` | `session/update` `agent_message_chunk` | message upserts by id | steal |
| Thoughts | `event` `loop.reasoning` | `agent_thought_chunk` | same | shape |
| Tool chrome | `harness.tool.start` / `.end` | `tool_call` / `tool_call_update` | omit/`null`/value merge | steal |
| Cancel | `cancel` request `{ok}` | `session/cancel` notification | same | shape |
| Usage / ctx chip | `prompt.usage` + `context` | `usage` + `usage_update`; extra `context` | first-class `usage_update` | steal |
| Ask / auto | `perm.set` | `session/set_mode` + `configOptions` | `configOptions` `mode` | shape |
| Permission prompt | `perm.ask` + `perm.decide` | agent→client `session/request_permission` | same + `title` / `subject` | steal |
| Always-allow | `decision: always` (this tool, session) | `allow_always` option | `allow_always` / `reject_always` | same |
| Slash list | `slash.list` (name, exclusive, aliases) | `available_commands_update` | same | shape |
| Slash run | extra `slash` (`help` without Run, `{title,body,error}`) | extra `slash` when advertised; else `"/name …"` in `session/prompt` | same | **extra** |
| Exclusive while busy | `slash` refuses | extra `slash` refuses when busy | not specified | **extra** |
| Steer | `steer` | — | — | **extra** |
| Ephemeral `/btw` | `prompt.ephemeral` | — | — | **rpc** |
| Compact / rewind | `compact`, `rewind` | — | — | **extra** |
| Model / effort | `model.*`, `effort.*` | `configOptions` + `set_config_option` | same | steal |
| Skills / plugins | `skill.*`, `plugin.list` | — | — | **extra** |
| Extra roots | `status`/`session` `{path,read_only}` | spawn cwd only | `additionalDirectories` (no ro/rw) | **extra** |
| Live status | `status` (`busy`, `ask_mode`, `pending_perm`, `procs`) | extra `status` / `proc.list` | `state_update` | **extra** |
| UI config | `extension.config` `{name:mowi}` | not an agent concern | not an agent concern | **rpc** |
| Goal / delegate events | `graph.goal.*`, `harness.delegate.*` | `plan` / thought / `_meta` | `plan_update` | **rpc** |
| `@path` attachments | host jail + `attached[]` | content blocks (image/resource) | same | shape |
| Client-owned FS / PTY | no | `fs/*`, `terminal/*` (v1) | FS removed; terminals display-only | **acp** |
| Concurrent control | worker vs control channel | not specified | not specified | **rpc** |

## Dual-run notes

A fixture that drives the same Engine through both `mow rpc` and `mow acp`
should assert:

1. A text prompt streams and ends (`end_turn` / `completed`).
2. Cancel unblocks the in-flight turn.
3. A write in ask/code mode surfaces a permission decision.
4. Linked slash names appear (`slash.list` vs `available_commands_update`).
5. Usage numbers are present on both wires.

It should **not** require steer, rewind, exclusive slash, extra-root chrome,
or `extension.config` of a generic ACP client. Those are optional methods on
the same `mow acp` connection (`agentCapabilities.experimental` /
`agentCapabilities.extras`: `steer`, `compact`, `rewind`, `skill.list`,
`skill.activate`, `plugin.list`, `transcript`, `status`, `context`,
`proc.list`, `ping`, `slash`). Power clients feature-detect; unknown
methods stay `-32601`. RPC still exposes them until mowi speaks ACP.
Theme / `extension.config` stay host-local. Ephemeral prompt stays
RPC-only. Exclusive slash is the extra `slash` method (busy refuse),
not `"/name"` inside `session/prompt`.

## What RPC steals (epoch 1, additive)

Keep mow method names. Feature-detect from `version.methods` / `features`.

1. **`update` notification** next to `event`. Typed `kind` (`token`,
   `thought`, `tool`, `usage`, `state`, `commands`) so a new host does not
   parse the Engine bus. `event` stays the source of truth.
2. **Richer `perm.ask`**. Additive `title` + `subject` (`tool_call` /
   `command`). `y` / `n` / `a` still map to `allow` / `deny` / `always`.
3. **`config.list`**. Catalog of `model` / `effort` / `perm` (and later
   chips) without forcing mowi to parse generic selects for safety.
   Typed `model.set` / `effort.set` / `perm.set` remain.

v2 stays behind `protocolVersion` negotiation on `mow acp`. Do not drop v1.
