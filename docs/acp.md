# `mow acp` + extras

Public host protocol is Agent Client Protocol v1 on `mow acp`. Leftovers that
ACP does not specify (steer, compact, rewind, skills/plugins, extra-root
ro/rw, exclusive slash, ephemeral `/btw`) are **optional methods on the same
connection**, capability-gated — not a second process and not `_mow/*`.

Generic ACP clients see only the standard. Mowi speaks ACP plus extras the
agent advertised on `initialize`. Session **mode** is ACP
`session/set_mode` (`modeId` ask|code) — confirm vs run, not read-only.
Tool confirmation is ACP **permission** (`session/request_permission`).
Session **approvals** (prompt|always) skip that overlay; still gated by
`--allow-write` / `--allow-shell` / `--read-only`. Distinct from mode.
`initialize` does not advertise `auth.logout`; credentials live in host
config, not an ACP login flow.

Sources: [ACP v1 overview](https://agentclientprotocol.com/protocol/v1/overview),
[v2 overview](https://agentclientprotocol.com/protocol/v2/overview),
[`ext/acp`](../ext/acp/README.md).

## Roles

| Surface | Process | Audience |
|---|---|---|
| `mow acp` | ACP agent on stdio + extras | editors, desktop, mowi |
| `delegate` | mow as ACP *client* | named peers (`extensions.acp.peers`) |
| `mow.Engine` | in-process library | Go embedders |

## Extras (`agentCapabilities.experimental`)

`steer`, `compact`, `rewind`, `skill.list`, `skill.activate`, `plugin.list`,
`transcript`, `status`, `context`, `proc.list`, `ping`, `slash`. Unknown
methods stay `-32601`. TUI theme / mode / approvals live in host
`$MOW_HOME/config.yaml` root `mowi:` — not an agent extra.

Ephemeral aside is an extra field on `session/prompt`. Exclusive slash is the
extra `slash` method (busy refuse) plus `exclusive` on
`available_commands_update`. Jail-safe `@path` expands on `session/prompt`.

v2 stays behind `protocolVersion` negotiation. Do not drop v1.

## Sessions

`mow acp` is **one Engine, one active ACP session**. `session/new` binds that
Engine JSONL id (so `--session` / `--continue` stay the ids operators already
use). A second `session/new` while bound is `-32600`. `session/close` unbinds;
the next `session/new` starts a **new** JSONL via `Engine.BeginSession`. Loading
a different on-disk id requires a new process (`mow acp --session ID`).
