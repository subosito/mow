# mow — extensions and packs

**Rule:** if a capability is not required for a read-only agent over compatible
LLM HTTP, it is detachable. Core protocols/runtime adapters live under `ext/`;
workflow/domain integrations live in the separate `packs/` module; heavy OTEL
dependencies live in a nested module. The Rust `mowi` sibling project is the
external TUI over `mow rpc`.

Customization modes:

1. **Configure** — YAML/env/skills; no code.
2. **Program** — `mow.Options`, custom tools/provider, `ext.Register*`.
3. **Link** — blank-import core extensions or optional packs into a binary.

## Layers and imports

| Layer | Imports | Examples |
|---|---|---|
| Public Engine | `github.com/subosito/mow` | `Engine`, `Run`, hooks, events, providers |
| Registration | `github.com/subosito/mow/ext` | `RegisterTool`, `RegisterCommand`, lifecycle hooks |
| Core extensions | `github.com/subosito/mow/ext/<name>` | acp, focus, media, rpc — privileged tier (may import internal/); one-pager in each `ext/<name>/README.md` |
| Optional packs | `github.com/subosito/mow/packs/<name>` | goal, review, ops, lsp, job, contextsink — `packs/<name>/README.md` |
| Heavy optional | `github.com/subosito/mow/packs/otel` | OTLP — `packs/otel/README.md` |

```go
import (
    "github.com/subosito/mow"
    _ "github.com/subosito/mow/ext/acp"
    _ "github.com/subosito/mow/packs/mcp"
    _ "github.com/subosito/mow/packs/contextsink"
    _ "github.com/subosito/mow/packs/goal"
    _ "github.com/subosito/mow/packs/otel"
)
```

Remove an import and the associated tools/subcommand/auto-wire disappear.

## Linked binaries

- `cmd/mow` is the sole full pack host: it links core extensions,
  goal/review/ops/lsp/job, and OTEL.
- The Rust `mowi` sibling project launches `mow rpc`, owns terminal
  presentation, and receives the registered command/tool events over RPC.
- `mow_agents` start the currently running executable (`os.Executable()`), so
  native ACP peers remain self-contained.

## Core extensions (`ext/`)

### ACP (`ext/acp`)

[Agent Client Protocol](https://agentclientprotocol.com) over JSON-RPC 2.0:

- `mow acp`: run the current host as an ACP agent.
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

### MCP (`packs/mcp`)

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

### Process / RPC / command hooks / eval

- `packs/proc`: `proc_start`, `proc_status`, `proc_stop` and `mow proc`. Stop
  signals the process group; log tails are size-capped.
- `ext/rpc`: JSON-lines prompt/event/cancel/status control plane. `mow rpc`
  always `Close`s the Engine on exit. Cancel/status use a dedicated channel so
  a full prompt queue cannot starve control methods; event deltas and prompt
  text are size-capped. RPC `"3"` adds the host methods an external UI needs
  without embedding Engine: `sessions`, `transcript`, `steer`, `slash.list`,
  `slash`, and a permission gate (`perm.set` / `perm.decide` answering
  `perm.ask` notifications for `write`, `edit`, `bash`). The gate is fail-open
  until a UI selects ask mode, so headless scripts are unchanged. `status` and
  `session` include `extra_roots` security metadata and configured
  `extra_roots_rw` / `extra_roots_ro` counts; they do not include repository
  presentation metadata. `capabilities.optional.features` dynamically lists
  optional packages that register host-facing facilities and their event
  types. Optional slash commands remain discoverable through `slash.list`.
- `packs/cmdhook`: Claude-style lifecycle shell hooks (`root` or `plugins` map,
  `min_turns`). Hooks re-register on every `BeforeNew` (no first-config pin);
  prior cmdhook hooks are cleared so profiles do not leak across Engines.
  Hermetic engines only see the current generation of hooks. Hook stdout/stderr
  are capped (~64KiB); diagnostics redact common secrets. Default is **fail-open**
  on timeout/non-2 exit (warn only); set `fail_closed: true` to block like exit 2.
- `packs/eval`: eval/replay fixtures and command (fixture size/case count capped).
  Optional — not blank-imported by stock `cmd/mow`. Import `github.com/subosito/mow/eval`
  from tests, or blank-import `packs/eval` for `mow eval run`.

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

### Extension lifecycle (`min_turns`)

MCP servers and command-hook plugins support optional `min_turns` (default `0`,
active from start). When `turn < N`, those hooks/tools stay dormant. There is
no `mow ext` / `/ext` command — enable or disable by config (or omit the
server / plugin).

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
  `Remove`, `Reset`, and deprecated `Delete` take that run lock so they cannot
  clobber a live run. `--force` on Remove only bypasses leftover
  `StatusRunning` after the lock is acquired. `Remove` also deletes the
  per-goal `events.jsonl` dir.
- Event logs (`events.jsonl`) rotate at ~4MiB; conflicted worktrees stay for
  manual inspection; parent-repo merges take `.git/mow-repo.lock`.

```bash
mow goal run --goal "Make CI green"
mow goal status --id NAME
mow goal run --goal "Make CI green"
```

### Review (`packs/review`)

Read-only two-pass code/security review (`review` and `sec` commands), with
text/JSON/JSONL/SARIF output and validated finding scope. Also registers
`/review` and `/sec` as interactive slash commands (see below). See
[review.md](review.md) and [packs/review/README.md](../packs/review/README.md).

CLI ensemble: `--reviewer` (repeatable or comma-separated; `--reviewers` is an
alias) for pass-one models, `--verifier` for the single pass-two judge.
Slash `/review` and `/sec` run against the session engine only — they do not
start an ensemble. ACP / `acp_delegate` is denied in the review jail.

## Interactive slash commands (`slash`)

A pack can own a command a user types into an interactive session. Registering
one in `init` is all it takes: every host that dispatches through the registry
— `mow tty` and the Rust `mowi` TUI over `mow rpc` — gains the command because the pack is linked,
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
stdout (so a piped stdout stays the report), while the Rust `mowi` host paints
the RPC events in its own terminal layout. One behavior, two presentations, no
duplicated flag parsing.

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

Stock slash set is **`/goal`**, **`/review`**, **`/sec`**. Those drive the
*live session Engine*. Do **not** register slash for:

- **job** — in-process clock (`mow job`). A `/job` that fires ticks is a
  footgun; list/check stay CLI.
- **ops** — catalog + tools + `mow ops run`. The model uses `ops_*` tools
  inside a tick; `/ops` would start or steer a daemon from chat.
- **contextsink**, **lsp**, **otel** — tools or config, not user-typed
  session commands.

### Ops (`packs/ops`)

Configured service profiles under `$MOW_HOME/ops/<name>/`: services, logs,
health, declared log patterns, allowlisted argv actions, incidents, dependencies,
runbooks, and peer-assisted remediation. No profile means no ops tools.

- `mow ops run NAME` uses `job.Daemon` with a fresh Engine per tick (the job
  pack owns and Closes it). Sub-second `every` is raised to 1s by job.
  That daemon is a separate process (job id `ops-<name>`); it is not a
  row in `mow job list`. Last tick is `$MOW_HOME/job/state/ops-<name>.json`.
  Two consecutive overlap skips open/update an incident
  `job-overlap:ops-<name>`.
- No `/ops` slash. Agent surface is the `ops_*` tools. Operator surface is
  `mow ops`.
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
and caps file bodies at 4 MiB. `$MOW_HOME/lsp.yaml` is capped at 1 MiB.
A cancelled or expired RPC kills a wedged server and returns without retrying
on the dead context; the next call starts a fresh process.

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
  Skips are counted in `$MOW_HOME/job/state/<id>.json`; `mow job list` shows LAST.
- Each tick builds a fresh `Engine` and closes it when the tick ends.
- A `every` shorter than 1s is raised to 1s. Cron is 5-field local time;
  `29 2` searches up to 8 years so leap days are not dropped.
- Done goals are reset via `goal.Store.Reset` (plan items return to pending)
  before the tick; blocked goals are skipped until `mow goal run --answer`.
- `$MOW_HOME/job/schedules.yaml` (or `--schedules`) must be a regular file,
  max 1 MiB / 64 entries. An explicit `--schedules` path that is missing is
  an error; only the default path falls back to `extensions.job`.
- Duplicate ids fail `mow job check` / `run`. Disabled entries are valid.
- Schedules load once at daemon start. `mow ops run` is not listed here.
- No `/job` slash and no job tools. Operator surface is `mow job` only.

### Context sink (`packs/contextsink`)

The full tool-result side channel, write side and read side together:

- **Write side** — results above `max_inline_bytes` are stored beside the
  session (`<sid>.tools/`) and replaced in live history with a short stub.
- **Read side** — `recall` (registered via `ext.RegisterTool`, so it
  needs no engine wiring): pattern search over the session's compaction
  archives and stored results (stored files carry a `stored ` snippet header),
  or recall of a stored stub (`id=…`, bounded window). It resolves
  the session dir from the engine at call time and is read-only, so it works
  in read-only prompts. Symlinks and non-regular files under the session
  archive/tools dirs are ignored. Stub previews redact common secret shapes;
  recovery via `recall` returns verbatim stored/archive text (product
  choice — the model needs faithful detail when explicitly recovering).
  Session tool I/O uses `O_NOFOLLOW` on Unix; other platforms use Lstat
  containment plus post-open regular-file checks (residual TOCTOU on hostile
  hosts — see `internal/session/safefile*` for the store and
  `packs/contextsink/safefile*` for pinned-disk reads in tests).
  Per-session retrieval budgets use deterministic LRU eviction (128 sessions);
  unrelated sessions search in parallel.

Storage is strictly session-scoped (search, recall, and the retrieval
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
results simply stay inline and no search tool exists. The stock `mow` binary
links it; library embeds opt in by blank-importing it. Config key
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

When `otel.endpoint` is set (or `MOW_OTEL_ENDPOINT` / `OTEL_EXPORTER_OTLP_ENDPOINT`),
the hook attaches OTLP/HTTP tracing and metrics. Empty endpoint means no exporter.
A non-empty endpoint is on; set `enabled: false` in the `otel:` config section
to force off despite the endpoint. Protocol is `http` (default);
`grpc` is reserved. `Shutdown` is idempotent, ends leftover spans, and flushes
queued telemetry (auto-wire cleanup uses a 5s timeout). Span error/status text
is redacted and length-capped. URL userinfo becomes an `Authorization` header
when none is set.

## TUI host

The Rust `mowi` sibling project/repository is the interactive terminal host.
It launches `mow rpc` and renders sessions, streaming, model/effort pickers,
tool approval, peer streams, and pack command results. See that project for
installation and release instructions.

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

Media ships as the linked pack `ext/media` (blank-import, like `acp`/`rpc`/
`focus`). Each tool registers only when its model id is configured under
`extensions.media.generate.*` / `extensions.media.understand.*`; `tools.enable` still gates visibility.
acks.

## Two tiers: `ext/` and `packs/`

Both tiers register the same way (blank import + `ext.Register*`) and both
detach the same way — delete one line from `cmd/mow/main.go` and the feature is
gone from the binary. That *link* boundary is what keeps core lean, and it is
the only property a pack must have.

They differ in what they may depend on:

| | `ext/` | `packs/` |
|---|---|---|
| Module | root | separate (`packs/go.mod`) |
| May import `internal/…` | **yes** — privileged first-party | **no** — public API only |
| Maintained | in lockstep with core | as an API consumer |
| Detach | one blank import | one blank import |

`ext/` is **privileged first-party code that happens to be optionally linked**,
not a public plugin surface. Being in `ext/` is not a promise the pack could
move to `packs/`. `ext/acp` and `ext/media` reach into `internal/` by design:
media needs the path-jail policy and the media HTTP client, which are
mid-evolution internals with no public contract.

This is deliberate. A same-module `internal/` import pins first-party code that
can be updated in the same commit. Exporting those types to make a pack "pure"
pins them for every future consumer, forever — the worse of the two pins, and
there is no versioning story for it (`packs/` consumes the root module at
`v0.0.0` through a `replace`). Do not export internals to prove a boundary;
export them when a second real consumer needs them.

`packs/` is the tier with the actual API contract, and that rule should be kept
true. It is convention, not compiler-enforced: Go's `internal/` rule is
import-path-prefix based, and `packs/` sits under `github.com/subosito/mow/`,
so the import compiles. Treat an `internal/` import from `packs/` as a bug.

When adding a pack: default to `packs/`. Use `ext/` only when it genuinely needs
core internals, and keep that surface narrow.

## Explore guards (`ext/focus`)

The soft anti-thrash heuristics are a linked pack, not core loop behavior.
Blank-importing `ext/focus` (the stock `mow` binary does) installs:

1. re-read caps — the same unchanged path via `read`, or `bash cat/sed/head/tail`
2. inventory caps — repeated `git status`/`ls`/`find`/`rg` degrade, then refuse
3. a soft block on destructive `git`/`rm` that would discard uncommitted work
4. productive bash (`go test`, `go build`, `git commit`, …) resets the streak
5. a nag after N consecutive explore-only turns

Remove the import and every one of them disappears with no residue in the
loop. The engine's own guards — `MaxTurns`, context cancel, `ErrStuck`, and the
identical-tool-call nudge in `internal/agent/repeat.go` — stay in core: those
are mechanism, not workflow opinion.

Tunable under `extensions.focus` (defaults reproduce the pre-move behavior):

```yaml
extensions:
  focus:
    explore_warn_every: 6      # nag after N explore-only turns
    reread_limit: 1            # re-reads of an unchanged path before stubbing
    inventory_limit: 2         # inventory calls before results degrade
    hard_inventory_limit: 4    # inventory calls before refusal
    degraded_result_limit: 2000 # cap (chars) on a degraded result body
```

A malformed section falls back to defaults rather than failing Engine
construction — the guards are an advisory lane, not a gate.

### Hooks it rides on

The pack uses `PreTool` (deny + stub message), `PostTool` (rewrite the result
body), and `AfterTurnDecide`. The last is the deciding form of the after-turn
hook:

```go
type AfterTurnDecisionFunc func(ctx context.Context, e AfterTurnEvent) (AfterTurnDecision, error)
```

`AfterTurnDecision.Inject`, when non-empty, is appended to history as a
synthetic user message before the next LLM call. It is a *sibling* of the
observer `AfterTurnFunc`, which keeps its signature — existing observers are
unaffected. A hook supplies text only, never a `Message`: the loop owns the
framing so an extension cannot forge assistant/tool roles and break tool_call
pairing. Multiple hooks compose by ordered concatenation.

