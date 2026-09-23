# mow — extensions

**Rule:** if a capability is not required for a read-only agent over compatible
LLM HTTP, it is detachable. Stock Engine *surfaces* (CLI, TTY, ACP) live under
`ext/`. Workflow/domain applications can live in external modules built on the
public API; [custom-host](https://github.com/subosito/mow) is one such application.

Customization modes:

1. **Configure** — YAML/env/skills; no code.
2. **Program** — `mow.Options`, custom tools/provider, `ext.Register*`.
3. **Link** — blank-import core extensions into a binary.

## Layers and imports

| Layer | Imports | Examples |
|---|---|---|
| Public Engine | `github.com/subosito/mow` | `Engine`, `Run`, hooks, events, providers |
| Registration | `github.com/subosito/mow/ext` | `RegisterTool`, `RegisterCommand`, lifecycle hooks |
| Core extensions | `github.com/subosito/mow/ext/<name>` | cli, tty, acp, proc, cmdhook, mcp, goal, workflow; one-pager in each `ext/<name>/README.md` |

```go
import (
    "github.com/subosito/mow"
    _ "github.com/subosito/mow/ext/acp"
    _ "github.com/subosito/mow/ext/proc"
    _ "github.com/subosito/mow/ext/mcp"
)
```

Remove an import and the associated tools/subcommand/auto-wire disappear —
including the system-prompt guidance for that capability. Extensions contribute
it via `ext.RegisterSystemSegment`, so the model only ever sees advice for
tools it actually has.

## Linked binaries

- `cmd/mow` is an import list over `ext/cli.Main`, linking tty, acp, proc,
  cmdhook, mcp, goal, and workflow. Drop an import and that surface is gone.
- External hosts launch `mow acp` (`ext/acp`), own presentation, and receive
  registered command/tool events over ACP + extras.
- Native `agents` (`model:`) start the currently running executable
  (`os.Executable()`), so native ACP peers remain self-contained.

## Core extensions (`ext/`)

### CLI (`ext/cli`)

Unix host skeleton. Import (not blank) and call `cli.Main`:

- Dispatcher: no-args TTY → `ext.DefaultInteractiveCommand` if registered, else usage.
  Free-form args become `mow run -p …`. Other names go to `ext.LookupCommand`.
- Commands: `run` (one-shot; `mow.NewHarness`), `models` (catalog list), `trust`,
  `doctor`, `approvals`, `version`, `help`.
- `cliutil` stays flags → Options / `NewHarness` (not a command, not a pack).

Stock binaries compose surfaces in `cmd/` by blank-importing `ext/tty`, `ext/acp`,
and core extensions. See [ext/cli/README.md](../ext/cli/README.md).

### TTY (`ext/tty`)

Optional line REPL (`mow tty`). Blank-import; drop it and the command is gone.
Does not import `ext/acp`. Same engine flags as `mow run`. In-session: `/model`,
`/btw`, `/help`, `/quit`, plus registered slash commands. See
[ext/tty/README.md](../ext/tty/README.md).

### ACP (`ext/acp`)

[Agent Client Protocol](https://agentclientprotocol.com) over JSON-RPC 2.0
(v1; see [acp.md](acp.md)):


- `mow acp`: run the current host as an ACP agent (session lifecycle,
  `session/prompt` + `session/update`, `available_commands_update`,
  agent→client `session/request_permission` for power tools, `usage_update`).
- `delegate`: send a task to a named external or native peer (ACP is the
  current peer protocol). Peer failures stay in the tool result, never as a
  parent-session abort — the run proceeds and the model decides how to react.
- Native peers (`model` on `peers[]`) support model, effort, system prefix,
  cwd, permissions, timeout, and optional `provider` (`llm.providers` name;
  omit inherits the host endpoint). Peer processes are reused by **agent + cwd +
  effective argv + permission_mode** (a model/policy change starts a new
  process). At delegate time, nil `allow_write` / `allow_shell` inherit the
  host Engine and are capped by it (never exceed host `AllowWrite` /
  `AllowShell`); workspace and `--extra-root` jail roots flow to the peer
  argv. Credentials are not forwarded as argv. `--read-only` is never
  combined with `--allow-write` / `--allow-shell` (CLI rejects that pair).
- External peers (`command` on `peers[]`) use the argv as written (put
  effort or other flags on the command — mow does not inject them). They
  use `permission_mode: reject|allow` (default **reject**)
  for agent→client `session/request_permission`. Native `model:` peers
  default **allow** — write/shell already inherited from the host, and
  the overlay cannot reach the TUI. Explicit yaml always wins. Reject returns ACP
  `{outcome:{outcome:"cancelled"}}`. Allow selects an `allow_once` /
  `allow_always` `optionId` from the request (never invents ids). Legacy peers
  whose argv already contains `--force` still auto-allow when
  `permission_mode` is omitted. The client answers `fs/*` and unknown methods
  with JSON-RPC errors and responds to `cursor/*` as unsupported so peers do
  not hang. If the peer process exits mid-RPC, the client fails immediately
  (does not wait for `timeout_sec`).
- On spawn/protocol failures, the last ~16KiB of peer stderr (secrets redacted)
  is appended to the error.
- `Engine.Close` (and `mow acp`, which defers it) SIGTERM then
  SIGKILL the peer process group so npx/node trees do not reparent to PID 1.
  `RegisterFromConfig` peers are process-global and are released when the
  last Engine of that BeforeNew generation closes. Idle eviction alone is not enough — a host
  that exits without Close used to leave peer process trees reparented to init.

```yaml
extensions:
  acp:
    peers:
      - name: peer-agent
        command: [peer-agent, --acp]
        timeout_sec: 300
        permission_mode: reject   # default; use allow for trusted peers
      - name: reviewer
        model: gpt-5-mini
        effort: high
        timeout_sec: 600
        system_prefix: "You are a reviewer."
        allow_write: true       # capped by host; omit to inherit
        read_only: false        # omit to inherit (!host write && !host shell)
        # provider: gateway     # native-only llm.providers name; omit inherits the host endpoint
```

### MCP (`ext/mcp`)

Both directions:

- configured client servers contribute `mcp_<server>_<tool>` tools;
- `mow mcp` exposes `mow_prompt` as an MCP stdio server.

No config means no client process. Supports stdio and streamable HTTP plus
bearer/OAuth modes. Stdio servers start during `BeforeNew` (host config only);
replacing a server closes the prior subprocess, and `Engine.Close` releases
transports for that engine's config generation. Subprocess stderr is captured
and redacted (not forwarded raw to the terminal). Wire I/O is bounded (line,
HTTP body, and tool text caps).

```yaml
extensions:
  mcp:
    mcpServers:
      fs:
        command: npx
        args: ["-y", "@modelcontextprotocol/server-filesystem", "/tmp"]
      remote:
        url: https://mcp.example/mcp
```

### Workflow (`ext/workflow`)

Host-owned versioned procedures with sequential typed JSON prompt/verify phases,
durable local run state, resume/block-on-interruption semantics, and `mow workflow`
plus `/workflow` surfaces. Definitions are registered by linked Go code; this is
not a script runtime or scheduler. See [ext/workflow/README.md](../ext/workflow/README.md)
and [orchestration.md](orchestration.md).

### Worktree (`ext/worktree`)

Dedicated git worktrees for parallel changes: `<repo>-worktrees/<name>` on
branch `wt/<name>`, one checkout per line of work. `mow worktree
list|add|remove|path`, `/worktree`, and `worktree_list` / `worktree_add` /
`worktree_remove` tools (add/remove need `--allow-shell`: they write outside the
jail). `remove` refuses uncommitted or unmerged work without `--force`. Open a
parallel session with `--workspace "$(mow worktree path NAME)"` or
`mowi --worktree NAME`. See [ext/worktree/README.md](../ext/worktree/README.md).

### Goal (`ext/goal`)

Durable outer loop over `Engine.Prompt`: checklist, evidence, budgets,
optional parallel nodes and worktree workers. `mow goal new|run|status|…`.
Step tools (`goal_report`, `goal_process_*`) are injected per step, not
globally. No `extensions.goal` section. See [ext/goal/README.md](../ext/goal/README.md).

### Process / command hooks / eval

- `ext/proc`: `proc_start`, `proc_status`, `proc_stop` (no CLI). Stop
  signals the process group; log tails are size-capped. Session-scoped procs
  die on `Engine.Close` unless `keep` is true.
- `ext/cmdhook`: Claude-style lifecycle shell hooks from host-owned Agent
  Plugins (`$MOW_HOME/plugins` and workspace profile `plugins/`) that ship
  `hooks/hooks.json`. There is no `extensions.cmdhook` section. Project
  `.mow/plugins` is skills-only. Plugin MCP and cmdhook set
  `CLAUDE_PLUGIN_ROOT` to the plugin folder and, when unset,
  `CLAUDE_CONFIG_DIR` to `$MOW_HOME` so Claude-shaped plugins (e.g. context-mode)
  store state under mow rather than `~/.claude`. A process or plugin-manifest
  `CLAUDE_CONFIG_DIR` still wins. Hooks re-register
  on every `BeforeNew` (no first-config pin); prior cmdhook hooks are cleared
  so profiles do not leak across Engines.
  Hermetic engines only see the current generation of hooks. Hook stdout/stderr
  are capped (~64KiB); diagnostics redact common secrets. Default is **fail-open**
  on timeout/non-2 exit (warn only).

### Extension lifecycle (`min_turns`)

MCP servers support optional `min_turns` (default `0`, active from start).
When `turn < N`, those tools stay dormant. Command-hook plugins activate as
soon as they are installed. There is no `mow ext` / `/ext` command — enable
or disable by config (or omit the server / plugin).

```yaml
extensions:
  mcp:
    mcpServers:
      filesystem:
        command: npx
        args: ["-y", "@modelcontextprotocol/server-filesystem", "/tmp"]
        min_turns: 0
```

## Configuration

Extension config is opaque under `extensions.<name>` and decoded with
`extcfg.DecodeSection`, or through `Engine.Extension`. Project config remains
subject to the core trust and security restrictions.

Engine setup supplies extensions with the resolved workspace,
`LoadUserConfig` policy, and config paths through `ext.SetupContext`. Extensions
that load workspace or user state should register with
`ext.RegisterBeforeNewContext` rather than infer authority from cwd or filenames.

## External applications

Domain workflows and full user applications are outside this module. See
[external application modules](https://github.com/subosito/mow) for the companion
application built on mow's public API.
