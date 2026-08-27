# acp

Agent Client Protocol (ACP) over JSON-RPC 2.0: run this host as an ACP agent, and optionally delegate to named peers.

## Link

Blank-import into a binary. Stock `cmd/mow` does:

```go
import _ "github.com/subosito/mow/ext/acp"
```

Drop the import and `mow acp` / `acp_delegate` disappear.

## Commands and tools

| Surface | Name |
|---|---|
| CLI | `acp` (`mow acp`) — ACP agent on stdin/stdout |
| Tool | `acp_delegate` — registered only when `agents` and/or `mow_agents` is non-empty |

No slash commands registered by this package. Linked slash commands from
other packs are advertised to the editor as `available_commands_update`
and dispatched when `session/prompt` text starts with `/name`.

`mow acp` speaks ACP v1 on stdio for an editor: initialize, **one**
active session (`session/new` binds the Engine JSONL; a second `new`
is refused until `session/close`, which then starts a fresh JSONL),
`session/prompt` + `session/update`, `session/request_permission`
(agent→client for power tools), usage_update, config options, and
terminals. Native `mow_agents` spawn the current executable
(`os.Executable()`), so the full `mow` host starts `mow acp`.
Peer processes are reused by agent + cwd + effective argv + permission mode.
`Engine.Close` (deferred by `mow acp`) SIGTERM then SIGKILL the
peer process group so delegated trees do not reparent to PID 1.

See [docs/acp.md](../../docs/acp.md) for extras on the same connection.


## Config (`extensions.acp`)

```yaml
extensions:
  acp:
    peer_idle_sec: 900          # drop idle peers; 0 = default 900; -1 = never by idle
    agents:                     # external ACP peers
      - name: peer-agent
        command: [peer-agent, --acp]
        dir: ""                 # default: host workspace
        timeout_sec: 300
        effort: high            # optional; appended as --reasoning-effort when the argv lacks it
        permission_mode: reject # reject (default) | allow
    mow_agents:                 # native mow peers (same product, other model)
      reviewer:
        model: gpt-5-mini       # required
        allow_write: true       # nil = inherit host (capped by host)
        allow_shell: false
        read_only: false        # nil = inherit (!host write && !host shell)
        timeout_sec: 600
        effort: high
        system_prefix: "You are a reviewer."
        dir: ""
        extra_args: []
```

`permission_mode` on `agents[]` is `reject` or `allow`. Names in `agents[]` and `mow_agents` must not collide.

## Docs

- [docs/extensions.md](../../docs/extensions.md) — ACP peers, native agents, permission handling
- [docs/architecture.md](../../docs/architecture.md) — binaries and `mow_agents`
- [docs/harness.md](../../docs/harness.md) — ACP request flow
