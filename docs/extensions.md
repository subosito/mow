# mow — extensions and packs

**Rule:** if a capability is not required for a read-only agent over compatible
LLM HTTP, it is detachable. Core protocols/runtime adapters live under `ext/`;
workflow/domain integrations live in the separate `packs/` module; heavy OTEL
and TUI dependencies live in nested modules.

Customization modes:

1. **Configure** — YAML/env/skills; no code.
2. **Program** — `mow.Options`, custom tools/provider, `ext.Register*`.
3. **Link** — blank-import core extensions or optional packs into a binary.

## Layers and imports

| Layer | Imports | Examples |
|---|---|---|
| Public Engine | `github.com/subosito/mow` | `Engine`, `Run`, hooks, events, providers |
| Registration | `github.com/subosito/mow/ext` | `RegisterTool`, `RegisterCommand`, lifecycle hooks |
| Core extensions | `github.com/subosito/mow/ext/<name>` | acp, mcp, proc, rpc, cmdhook, eval |
| Optional packs | `github.com/subosito/mow/packs/<name>` | goal, review, ops, lsp, job, contextsink |
| Heavy optional | `github.com/subosito/mow/packs/otel`, `…/packs/mowi` | OTLP and TUI |

```go
import (
    "github.com/subosito/mow"
    _ "github.com/subosito/mow/ext/acp"
    _ "github.com/subosito/mow/ext/mcp"
    _ "github.com/subosito/mow/packs/contextsink"
    _ "github.com/subosito/mow/packs/goal"
    _ "github.com/subosito/mow/packs/lsp"
    _ "github.com/subosito/mow/packs/otel"
)
```

Remove an import and the associated tools/subcommand/auto-wire disappear.

## Linked binaries

- `cmd/mow` links core extensions, goal/review/ops/lsp/job, and OTEL; no TUI.
- `packs/mowi/cmd/mowi` links the full TUI and the same registered commands.
- `mowi acp`, `mowi goal`, `mowi review`, `mowi ops`, etc. dispatch through
  `ext.LookupCommand`, just like `mow`.
- `mow_agents` start the currently running executable (`os.Executable()`), so
  either binary works standalone for native ACP peers.

## Core extensions (`ext/`)

### ACP (`ext/acp`)

[Agent Client Protocol](https://agentclientprotocol.com) over JSON-RPC 2.0:

- `mow acp` / `mowi acp`: run the current host as an ACP agent.
- `acp_delegate`: delegate to named external or native peers.
- Native `mow_agents` support model, effort, system prefix, cwd, permissions,
  and timeout. Peer processes are reused by **agent + cwd + effective argv +
  permission_mode** (a model/policy change starts a new process). At delegate
  time, nil `allow_write` / `allow_shell` inherit the host Engine and are capped
  by it (never exceed host `AllowWrite` / `AllowShell`); workspace and
  `--extra-root` jail roots flow to the peer argv. Credentials are not
  forwarded as argv. `--read-only` is never combined with `--allow-write` /
  `--allow-shell` (CLI rejects that pair).
- External `agents[]` use `permission_mode: reject|allow` (default **reject**)
  for agent→client `session/request_permission`. Reject returns ACP
  `{outcome:{outcome:"cancelled"}}`. Allow selects an `allow_once` /
  `allow_always` `optionId` from the request (never invents ids). Legacy peers
  whose argv already contains `--force` still auto-allow when
  `permission_mode` is omitted. The client answers `fs/*` and unknown methods
  with JSON-RPC errors and responds to `cursor/*` as unsupported so peers do
  not hang. If the peer process exits mid-RPC, the client fails immediately
  (does not wait for `timeout_sec`).
- On spawn/protocol failures, the last ~16KiB of peer stderr (secrets redacted)
  is appended to the error.

```yaml
extensions:
  acp:
    agents:
      - name: peer-agent
        command: [peer-agent, --acp]
        timeout_sec: 300
        permission_mode: reject   # default; use allow for trusted peers
    mow_agents:
      reviewer:
        model: gpt-5-mini
        effort: high
        system_prefix: "You are a reviewer."
        allow_write: true       # capped by host; omit to inherit
        read_only: false        # omit to inherit (!host write && !host shell)
```

### MCP (`ext/mcp`)

Both directions:

- configured client servers contribute `mcp_<server>_<tool>` tools;
- `mow mcp` / `mowi mcp` expose `mow_prompt` as an MCP stdio server.

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

### Process / RPC / command hooks / eval

- `ext/proc`: `proc_start`, `proc_status`, `proc_stop` and `mow proc`. Stop
  signals the process group; log tails are size-capped.
- `ext/rpc`: JSON-lines prompt/event/cancel/status control plane. `mow rpc`
  always `Close`s the Engine on exit. Cancel/status use a dedicated channel so
  a full prompt queue cannot starve control methods; event deltas and prompt
  text are size-capped.
- `ext/cmdhook`: Claude-style lifecycle shell hooks (`root` or `plugins` map,
  `min_turns`). Hooks re-register on every `BeforeNew` (no first-config pin);
  prior cmdhook hooks are cleared so profiles do not leak across Engines.
  Hermetic engines only see the current generation of hooks. Hook stdout/stderr
  are capped (~64KiB); diagnostics redact common secrets. Default is **fail-open**
  on timeout/non-2 exit (warn only); set `fail_closed: true` to block like exit 2.
- `ext/eval`: eval/replay fixtures and command (fixture size/case count capped).

```yaml
extensions:
  cmdhook:
    fail_closed: false   # default: timeout/fail does not block
    root: /path/to/plugin
    # or:
    plugins:
      policy:
        root: /path/to/policy
        fail_closed: true  # timeouts and exec errors deny the tool
        min_turns: 0
```

### Extension lifecycle & turn control (`mow ext` / `/ext`)

Extensions (such as MCP servers and command hook plugins) support optional `min_turns` thresholds and manual runtime activation control:

- **`min_turns: N`**: specifies turn activation threshold (default `0`, active from start). When `turn < N`, hooks/tools remain dormant.
- **`mow ext` / `/ext`**: inspect or toggle extensions at runtime:
  - `mow ext list` or `/ext list`: list registered extension instances and status.
  - `mow ext on <name>` or `/ext on <name>`: manually enable extension `<name>`, overriding `min_turns`.
  - `mow ext off <name>` or `/ext off <name>`: manually disable extension `<name>`.

```yaml
extensions:
  cmdhook:
    plugins:
      context-mode:
        root: /path/to/context-mode
        hooks_file: hooks/hooks.json
        min_turns: 5
  mcp:
    mcpServers:
      filesystem:
        command: npx
        args: ["-y", "@modelcontextprotocol/server-filesystem", "/tmp"]
        min_turns: 0
```

## Optional packs (`packs/`)

### Goal (`packs/goal`)

Durable multi-step workflow around `Engine.Prompt`: checklist state, evidence,
budgets, optional parallel nodes, worktree workers, process tools, and graph
events. State lives under `$MOW_HOME/goals`:

- Atomic JSON writes with per-goal lock files (portable flock / O_EXCL) plus
  in-process directory locks.
- One run per goal id across processes via `<id>.run.lock` and owner PID/host
  metadata; dead owners heal `StatusRunning` → `Pending` for safe resume.
- Event logs (`events.jsonl`) rotate at ~4MiB; conflicted worktrees stay for
  manual inspection; parent-repo merges take `.git/mow-repo.lock`.

```bash
mow goal run --goal "Make CI green"
mow goal status --id NAME
mowi goal run --goal "Make CI green"
```

### Review (`packs/review`)

Read-only two-pass code/security review (`review` and `sec` commands), with
text/JSON/JSONL/SARIF output and validated finding scope. Also registers
`/review` and `/sec` as interactive slash commands (see below). See
[review.md](review.md).

## Interactive slash commands (`slash`)

A pack can own a command a user types into an interactive session. Registering
one in `init` is all it takes: every host that dispatches through the registry
— `mow tty` and the mowi TUI — gains the command because the pack is linked,
and loses it when the blank import is dropped. No host names a pack.

```go
import "github.com/subosito/mow/slash"

func init() {
    slash.Register(slash.Command{
        Name:      "review",
        Summary:   "AI-assisted code review of a diff or paths (advisory)",
        Usage:     "…shown for /review help…",
        Exclusive: true, // drives the session engine; hosts refuse it mid-turn
        Run: func(ctx context.Context, req slash.Request) (slash.Result, error) {
            // req.Engine is the session's live engine: the command runs
            // against the model the user is already talking to, rather than
            // starting a child process.
            return slash.Result{Title: "…one line…", Body: "…full output…"}, nil
        },
    })
}
```

The contract is presentation-free on purpose. `Run` returns text; the host
decides how to paint it — `mow tty` prints the title on stderr and the body on
stdout (so a piped stdout stays the report), while mowi paints a status chip
and a framed transcript section and sets `Color: false` so raw ANSI does not
fight its layout. One behavior, two presentations, no duplicated flag parsing.

Rules worth knowing:

- **Built-ins win.** A pack cannot capture `/quit`, `/clear`, `/model`, … — a
  wedged session must always be exitable.
- **Unregistered tokens are not commands.** They fall through to the model, so
  a line like `/tmp is full` is still a sentence.
- **Help is free.** `/<name> help` (also `-h`, `--help`, `?`) prints `Usage`
  without invoking `Run`, so it works with no engine and costs no tokens. A
  bare `/<name>` runs the command's default behavior, and a path argument that
  merely contains "help" is not a help request.
- **Slash ≠ tool ≠ subcommand.** A slash command is host-side and user-typed:
  the model cannot call it, and it has no process exit code. Commands that need
  a CI contract keep a `ext.RegisterCommand` subcommand as well; `packs/review`
  registers both and shares the middle.

### Ops (`packs/ops`)

Configured service profiles under `$MOW_HOME/ops/<name>/`: services, logs,
health, declared log patterns, allowlisted argv actions, incidents, dependencies,
runbooks, and peer-assisted remediation. No profile means no ops tools.

- `mow ops run NAME` uses `job.Daemon` with a fresh Engine per tick (the job
  pack owns and Closes it). Sub-second `every` is raised to 1s by job.
- Log reads refuse symlinks/non-regular files and redact common secret shapes.
- Incidents are atomic JSON (fsync + rename), id-jailed, size-capped.
- Health probes are http/https only, ignore `HTTP_PROXY`, cap timeout at 30s,
  re-check redirects, and dial only loopback IPs for localhost/loopback URLs.
  `allowed_hosts` still trusts DNS for those names.
- Actions are operator argv lists (no shell). Any declared key may run, with a
  60s timeout. `ops_action` still requires `--allow-shell`.

### LSP (`packs/lsp`)

Opt-in language-server tools (`lsp_hover`, `lsp_definition`) and post-edit
`textDocument/diagnostic`. Requires an operator-installed/configured language
server. No config means no process. Tools declare `ReadOnly()` so they stay
available in read-only prompts.

Paths resolve through the engine path jail when available (same boundary as
`read`/`write`); hosts without an engine in context fall back to containment
under the configured `root`. RPC framing is bounded (1 MiB frames, capped
header lines and skipped frames); `didOpen` rejects symlinks/non-regular files
and caps file bodies at 4 MiB.

Diagnostics are sorted by severity, capped at `mow.MaxLSPDiagnostics`, attached
to successful write/edit results, and emitted as `harness.lsp.diagnostics`.
Post-edit pulls use their own deadline (`diagTimeout`, default 10s) and never
fail a successful edit when the server is slow or down.

```yaml
extensions:
  lsp:
    command: gopls
    args: [serve]
    root: .
```

### Job (`packs/job`)

Interval/cron prompt or goal jobs. Job depends on goal; ops uses job for daemon
runs.

- Same id never overlaps an active tick (later ticks are skipped, not queued).
- Each tick builds a fresh `Engine` and closes it when the tick ends.
- A `every` shorter than 1s is raised to 1s. Cron is 5-field local time;
  `29 2` searches up to 8 years so leap days are not dropped.
- Done goals are reset via `goal.Store.Reset` (plan items return to pending)
  before the tick; blocked goals are skipped until `mow goal run --answer`.
- `$MOW_HOME/job/schedules.yaml` (or `--schedules`) must be a regular file,
  max 1 MiB / 64 entries. An explicit `--schedules` path that is missing is
  an error; only the default path falls back to `extensions.job`.
- Duplicate ids fail `mow job check` / `run`. Disabled entries are valid.

### Context sink (`packs/contextsink`)

The full tool-result side channel, write side and read side together:

- **Write side** — results above `max_inline_bytes` are stored beside the
  session (`<sid>.tools/`) and replaced in live history with a short stub.
- **Read side** — `context_search` (registered via `ext.RegisterTool`, so it
  needs no engine wiring): pattern search over the session's compaction
  archives and stored results (stored files carry a `stored ` snippet header),
  or get-by-id fetch of a stored body (`id=…`, bounded window). It resolves
  the session dir from the engine at call time and is read-only, so it works
  in read-only prompts. Symlinks and non-regular files under the session
  archive/tools dirs are ignored. Stub previews redact common secret shapes;
  recovery via `context_search` returns verbatim stored/archive text (product
  choice — the model needs faithful detail when explicitly recovering).
  Session tool I/O uses `O_NOFOLLOW` on Unix; other platforms use Lstat
  containment plus post-open regular-file checks (residual TOCTOU on hostile
  hosts — see `internal/session/safefile*` for the store and
  `packs/contextsink/safefile*` for pinned-disk reads in tests).
  Per-session retrieval budgets use deterministic LRU eviction (128 sessions);
  unrelated sessions search in parallel.

Storage is strictly session-scoped (search, get-by-id, and the retrieval
budget are all pinned to the engine's own `SessionDir`+`SessionID` — never a
sibling session's), bounded (64 files / 32 MiB total, 8 MiB per file), and
pruned.

The pack registers entirely through the generic ext surface — the write side
via `ext.RegisterPostTool`, the read side via `ext.RegisterTool` — with no
context-specific engine slot. Hook ordering is plain registration order; the
engine's event emitter runs before all hooks, so hosts still receive full tool
bodies on `EventToolEnd` even when history carries a stub.

The pack emits metadata-only observability events:

- `harness.contextsink.store`: `tool`, `tool_call_id`, `stored_id`,
  `original_bytes`, and `inline_bytes` (the replacement stub size).
- `harness.contextsink.recover`: `tool`, optional `stored_id`,
  `recovered_bytes`, and `recovery_mode` (`id` or `pattern`).

Neither event includes stored or recovered content. Sum
`original_bytes - inline_bytes` to estimate bytes removed from subsequent
model context, and compare that with `recovered_bytes` to see how much was
brought back on demand. When the OTEL pack is configured, it exports these as
`mow.contextsink.stored_results`, `mow.contextsink.saved_bytes`, and
`mow.contextsink.recovered_bytes` counters. Without the pack,
results simply stay inline and no search tool exists. Stock binaries (`mow`,
`mowi`) link it; library embeds opt in by blank-importing it. Config key
`extensions.contextsink`.

```yaml
extensions:
  contextsink:
    enabled: true           # required; default: off
    max_inline_bytes: 8000  # above this → store + stub (default; capped at 8 MiB)
```

## OpenTelemetry (`packs/otel`)

Nested module so OTEL/grpc/protobuf dependencies do not enter a library-only
embed. Blank import registers an Engine-construction hook:

```go
import _ "github.com/subosito/mow/packs/otel"
```

When `otel.enabled: true` and `otel.endpoint` are configured, the hook attaches OTLP/HTTP tracing and
metrics; no endpoint means no exporter.

## TUI (`packs/mowi`)

Nested module so Bubble Tea/Lip Gloss/Chroma/Goldmark dependencies remain
optional. Build/install:

```bash
(cd packs/mowi && go build -o ../../bin/mowi ./cmd/mowi)
go install github.com/subosito/mow/packs/mowi/cmd/mowi@latest
```

The TUI supports sessions, streaming, model/effort pickers, tool approval,
peer streams, goal/review integration, and the same pack command surface as
`mow`.

## Configuration

Pack config is opaque under `extensions.<name>` and decoded with public helpers:

```go
var cfg Config
ok, err := extcfg.DecodeSection("name", paths, &cfg)
```

or through an Engine:

```go
err := eng.Extension("name", &cfg)
```

Project config remains subject to the core trust/security restrictions.

## Model catalog filtering

Callable selectors use the filtered catalog:

- `object: "model_group"` rows are discovery-only and excluded;
- non-chat facets and unsupported wires are excluded;
- aliases/composites that publish `wires` but omit `wire` receive a derived
  preferred chat wire, preserving selector labels and correct switching.

## Media lanes

Media stays a side lane to the chat loop:

| Tool | Endpoint / behavior |
|---|---|
| `generate_image` | image generation → workspace file |
| `generate_speech` | speech generation → workspace file |
| `generate_video` | submit/poll video generation |
| `understand_image` | chat with image parts |
| `understand_voice` | transcription endpoint |
| `understand_video` | chat with video parts |

Media tools are enabled/configured independently of extension packs.
