# acp

Agent Client Protocol (ACP) over JSON-RPC 2.0: run this host as an ACP agent, and optionally delegate to named peers.

## Link

Blank-import into a binary. Stock `cmd/mow` does:

```go
import _ "github.com/subosito/mow/ext/acp"
```

Drop the import and `mow acp` / `delegate` disappear.

## Commands and tools

| Surface | Name |
|---|---|
| CLI | `acp` (`mow acp`) — ACP agent on stdin/stdout |
| Tool | `delegate` — registered only when `peers` is non-empty |

No slash commands registered by this package. Linked slash commands from
other packs are advertised to the editor as `available_commands_update`
and dispatched when `session/prompt` text starts with `/name`.

`mow acp` speaks ACP v1 on stdio for an editor: initialize, **one**
active session (`session/new` binds the Engine JSONL; a second `new`
is refused until `session/close`, which then starts a fresh JSONL),
`session/prompt` + `session/update`, `session/request_permission`
(agent→client for power tools), usage_update, config options, and
terminals. `usage_update.used.inputTokens` is context occupancy (last
model call's input, or the post-compaction estimate) — the figure a
client paints as used/window. `usage_update.billed` is the run sum
`{inputTokens, outputTokens, cachedReadTokens}` and is not occupancy.
It is sent after every model call (occupancy) and again at run end
(occupancy plus billed). Native peers (`model:`) spawn the current executable
(`os.Executable()`), so the full `mow` host starts `mow acp`.
Peer processes are reused by agent + cwd + effective argv + permission mode.
`Engine.Close` (deferred by `mow acp`) SIGTERM then SIGKILL the
peer process group so delegated trees do not reparent to PID 1.

See [docs/acp.md](../../docs/acp.md) for extras on the same connection.

## Connection lifecycle

- Catalog-backed configuration changes use an ordered, bounded worker queue;
  slow provider metadata must not block ping, cancellation, or permission replies.
- Output-write failures terminate the connection and cancel its active work.
  Permission requests fail immediately if they cannot be written to the client.
- A peer whose stdout closes is not reusable, even if its process is still alive.
  Pending requests fail promptly; duplicate responses cannot block the reader.
- A response already delivered by an exiting peer wins over the process-exit
  notification. Buffered stdout gets a bounded drain opportunity before an
  unanswered request is reported as a process failure.
- Connection failures do not automatically replay prompts: earlier tool work may
  have side effects. Provider errors remain distinct from ACP transport failures.


## Config (`extensions.acp`)

```yaml
extensions:
  acp:
    peer_idle_sec: 900          # drop idle peers; 0 = default 900; -1 = never by idle
    peers:
      - name: peer-agent        # external ACP peer
        command: [peer-agent, --acp]
        dir: ""                 # default: host workspace
        timeout_sec: 300        # default 300 for command peers
        permission_mode: reject # reject (default) | allow
      - name: reviewer          # native mow peer
        model: gpt-5-mini       # required (exclusive with command)
        dir: ""
        timeout_sec: 600        # default 600 for model peers
        effort: high            # --effort on mow acp
        allow_write: true       # nil = inherit host (capped by host)
        allow_shell: false
        read_only: false        # nil = inherit (!host write && !host shell)
        system_prefix: "You are a reviewer."
        extra_args: []
```

`command` and `model` are exclusive. `permission_mode` (`reject` | `allow`) is how the parent answers the peer's `session/request_permission`. Omitted: native `model:` peers allow; external `command:` peers reject. Names in `peers[]` must not collide.

## Docs

- [docs/extensions.md](../../docs/extensions.md) — ACP peers, native agents, permission handling
- [docs/architecture.md](../../docs/architecture.md) — binaries and native peers
- [docs/harness.md](../../docs/harness.md) — ACP request flow
