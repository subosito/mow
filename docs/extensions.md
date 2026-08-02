# mow — core vs extensions

**Rule:** if it is not required for *“read-only agent on a repo via an OpenAI-compatible (or Anthropic) API,”* it is an **extension**, a **host UI concern**, or a **later pack** — not default core.

Customization modes (see [harness.md](harness.md)):

1. **Configure** — yaml, env, skill markdown (no code).  
2. **Program** — `ext.RegisterTool` / hooks / `RegisterCommand` / custom binary / `mow.Engine`.

---

## Decisions (this doc)

| Decision | Choice | Why |
|----------|--------|-----|
| Public API | `mow` + `ext` (+ packs) | Small stability surface for embedders |
| Implementation | `internal/*` | Loop/llm/tools not a compatibility surface |
| UIs & protocols | `ext/<name>` packs | Optional; not required for `Prompt()` |
| CLI subcommands | Pack-owned via `RegisterCommand` | Unlink pack → command disappears |
| Core CLI | `run`, `tty`, `version`, `help` only | Stock thin shell |
| ACP | Pack (`ext/acp`), not core | Standard editor + peer-harness wire |
| Sub-agents | Not core loop; use ACP delegate or multi-`Engine` | Single loop in core |
| Media tools | Side-lane `generate_*` / `understand_*` | Chat model stays primary; filesystem is I/O |
| Tool naming | Verbs `generate_image`, `understand_image` | Aligns with config `llm.generate` / `llm.understand` |
| Optional attribution labels | `X-Mow-*` | Labels only; plain providers ignore them |
| Lifecycle hooks | Full agent lifecycle in core (`ext.Register*` / `mow.Hooks`) | Optimizers (context-mode-style) plug in; no product pack required |

---

## Layering

```text
┌─────────────────────────────────────────────┐
│  External host UI (desktop, channel bots, …)│  goals board, multi-agent dashboards
├─────────────────────────────────────────────┤
│  ext/* packs (blank-import into binary)     │  acp, rpc, mcp, lsp, goal, …
├─────────────────────────────────────────────┤
│  Public: mow.Engine + ext registration      │  Prompt, tools, hooks, commands
├─────────────────────────────────────────────┤
│  internal/*                                 │  agent loop, llm, tools, config, …
└─────────────────────────────────────────────┘
```

| Layer | Owns |
|-------|------|
| **Public core** | `Engine.Prompt`, secure defaults, sessions, stream, skills, media tools (when configured) |
| **ext registration** | Tools, hooks, CLI commands, BeforeNew hooks |
| **Packs** | ACP, RPC, goal, MCP, LSP, job, ops (monitor+remediate), proc, … |
| **Host UI** | Goals board, collab, multi-agent roster (unless promoted to a pack) |

---

## Layout: what belongs where

```text
mow/                   # Engine API (library core)
cliutil/               # CLI helpers — flags → Engine (NOT a pack)
packcfg/               # decode extensions.<name> (NOT a pack)
ext/
  ext.go               # registration API: Tool, hooks, RegisterCommand, BeforeNew
  rpc/                 # pack: JSON-lines + subcommand "rpc"
  acp/                 # pack: ACP + "acp" + acp_delegate
  goal/                # pack: outer-loop goals + "goal"
  job/                 # pack: interval jobs (`mow job`)
  ops/                 # pack: ops profiles (logs, restart, incidents, peer fixes)
  mcp/                 # pack: MCP → tools
  lsp/                 # pack: LSP hover/definition (gopls, …)
cmd/mow/               # thin binary: run/tty + blank-import packs
```

| Path | Is a pack? | Role |
|------|------------|------|
| `ext/<name>` feature dirs | **Yes** | Blank-import → subcommand/tools |
| `ext` (root) | No | Registration surface for packs & integrators |
| `cliutil` | No | Shared CLI flags / help for any host binary |
| `packcfg` | No | Decode `extensions.<name>` for packs (BeforeNew) |
| `mow` | No | Core harness API |

Pack import: `github.com/subosito/mow/ext/<name>`.  
Helpers: `github.com/subosito/mow/cliutil`, `github.com/subosito/mow/packcfg`.  
Config section: `extensions.<name>` via `eng.Extension("name", &dst)` or `packcfg.DecodeSection`.

### CLI ownership (subcommand = pack)

Packs register in `init`:

```go
// e.g. ext/acp/cmd.go
func init() {
    ext.RegisterBeforeNew(RegisterFromConfig) // optional: tools before New
    ext.RegisterCommand(ext.Command{
        Name:    "acp",
        Summary: "ACP agent on stdin/stdout",
        Run:     runCmd,
    })
}
```

Stock binary only blank-imports packs:

```go
// cmd/mow/main.go
_ "github.com/subosito/mow/ext/acp"
_ "github.com/subosito/mow/ext/goal"
_ "github.com/subosito/mow/ext/job"
_ "github.com/subosito/mow/ext/lsp"
_ "github.com/subosito/mow/ext/mcp"
_ "github.com/subosito/mow/ext/ops"
_ "github.com/subosito/mow/ext/rpc"
```

| Action | Effect |
|--------|--------|
| Remove `_ "…/ext/acp"` | `mow acp` gone; help line gone; `acp_delegate` not registered |
| Add a new pack + import | Subcommand appears automatically |

Core keeps: **`run`**, **`tty`**, **`version`**, **`help`**.  
Default interactive (no args + TTY): only if a linked pack sets `DefaultInteractive`.

Shared flags for any Engine CLI: `cliutil.EngineFlags` → `NewEngine()` (runs `ext.BeforeNew` first).

---

## Config: `extensions.*`

Core yaml stays agent/LLM-oriented. Pack knobs are opaque blobs:

```yaml
extensions:
  acp:
    # External peers (any ACP-speaking command)
    agents:
      - name: peer-agent
        command: [env, ANTHROPIC_MODEL=claude-sonnet-4, npx, -y, "@agentclientprotocol/claude-agent-acp"]
    # Native mow multi-model peers (expands to: mow acp --model …)
    mow_agents:
      peer-agent:
        model: gpt-5-mini
        system_prefix: "You are a reviewer. Focus on actionable findings."
  # other packs: job, mcp, lsp, …
```

- Stored as YAML nodes under `extensions` (internal config).  
- Decode: `eng.Extension("acp", &cfg)` (public; no need to import `internal/config`).  
- Example file: [`internal/config/mow.yaml.example`](../internal/config/mow.yaml.example).

### `extensions.acp`

Named **agents** are what the model calls via `acp_delegate` (arg `agent`, alias
`subagent`). Under the hood each is an ACP peer process. Coming from harnesses
that say “subagent”: same idea — a named helper you **delegate** work to.

| Field | Meaning |
|-------|---------|
| `peer_idle_sec` | Drop idle peers after N seconds (default 900; `-1` = never). Always drop if process not alive. |
| `agents[]` | **External** peers: full command that speaks ACP on stdio |
| `agents[].name` | Id for `acp_delegate` arg `agent` |
| `agents[].command` | Peer argv |
| `agents[].dir` | Optional cwd (default: workspace) |
| `agents[].timeout_sec` | Cap per delegated prompt (default 300) |
| `agents[].effort` | Optional; appends `--reasoning-effort` when the peer CLI accepts it |
| `mow_agents` | **Native** multi-model mow peers (map name → spec). Expands to `mow acp --model …` |

#### `mow_agents.<name>` fields

| Field | Default | Meaning |
|-------|---------|---------|
| `model` | *(required)* | Model id for the peer `mow acp` process |
| `allow_write` | `true` | Pass `--allow-write` |
| `allow_shell` | `true` | Pass `--allow-shell` |
| `timeout_sec` | `600` | Cap per delegated prompt (longer default than external agents) |
| `effort` | *(omit)* | Pass `--effort <value>` to the peer |
| `system_prefix` | *(omit)* | Prepend identity or role text to the peer system prompt |
| `dir` | workspace | Peer working directory |
| `extra_args` | — | Extra argv after the standard flags (advanced) |

Names must not collide between `agents` and `mow_agents`. When either list is
non-empty, `RegisterFromConfig` (via `BeforeNew`) registers tool **`acp_delegate`**.

### `ext/goal` (outer loop + executor)

Multi-step goals **around** `Engine.Prompt` — not a second core agent loop.

```text
Runner (durable state, checklist, events)
  → Executor.RunStep (one Prompt + tools)
      → Engine.PromptWith + goal_report / process tools
```

```bash
mow goal new --id fix-ci --goal "Make CI green"
mow goal run --id fix-ci --model …        # or: mow goal run --goal "…"
mow goal status --id fix-ci
mow goal list
```

State: `$MOW_HOME/goals/<id>.json` (`summary`, `plan`, `session_id`, tokens).  
Events: `$MOW_HOME/goals/<id>/events.jsonl`.  
Embed: `goal.Runner{Engine, Store}.RunSpec(ctx, spec)`; optional `goal.Executor` for hosts.

**Parallel nodes with a join (opt-in):** `Spec.ParallelMax = N` (>1) runs up to
N independent *pending* checklist items as concurrent sub-steps, then joins
(item statuses + evidence + summaries merged back into the parent state before
the normal outcome handling). Because one `mow.Engine` serializes `Prompt`
calls, this requires `Runner.EngineFactory func() (*mow.Engine, error)` — a
fresh engine per sub-step, built like `goal.RunParallel`'s factory. Without a
factory (or with `ParallelMax` 0/1) the runner is sequential, unchanged.

**Worktree workers (opt-in):** a plan item with `Worker: goal.WorkerWorktree`
runs in its own `git worktree` on a `mow-wt-<goal>-<item>` branch, commits on
success, and merges back into the goal workspace with `--no-ff` — human SWE
primitives instead of asking concurrent agents politely not to collide. It
requires `Runner.WorktreeEngineFactory func(dir string) (*mow.Engine, error)`
(an engine whose `Workspace` is the checkout, so the sub-engine's tools and
path jail operate inside it), and composes with `ParallelMax`: work happens in
parallel, while the operations that touch the parent repo (worktree add/remove,
merge) are serialized because git locks the parent index.

Failure modes are deliberate:

| Situation | Behavior |
|-----------|----------|
| Not a git repo / no factory / detached HEAD | Runs as an ordinary step, with a note on the event stream — missing git never fails a goal |
| Step failed | Worktree discarded, not merged (a known-bad tree never reaches the base branch) |
| No file changes | Success, no commit, no branch left behind |
| **Merge conflict** | `OutcomeEscalate` → `StatusBlocked` + a question naming the branch and path; merge aborted so the parent stays clean, **worktree preserved** for the human. Conflicts are never resolved automatically |

Merged steps attach a bounded `git diff --stat` summary to the step result, so
the diff is reviewable before anyone looks at the branch.

**Checklist (recommended for multi-part goals):**

1. `goal_report status=continue plan=[{id,title,status:pending},…]`
2. Work one item → `goal_report status=continue item_id=… item_status=done`
3. When all items done/skipped → `goal_report status=done summary=…`  
   (`status=done` is **rejected** while checklist items remain pending.)

**Completion (any of):**

- tool **`goal_report`** (only during `goal run` steps) — preferred  
- fenced **`goal-status`** JSON  
- markers `GOAL_DONE` / `GOAL_FAILED:`

**Long-lived processes (servers, mocks):**

- `goal_process_start` / `goal_process_status` / `goal_process_stop`  
  (pid + log under `$MOW_HOME/goals/<id>/procs/`)  
- Do not nest `mow run` inside bash; do not leave servers in the bash foreground.

`mow goal run` prints tool progress on stderr; on exit prints `file:` / `events:` and resume hints.

| Status | Re-run |
|--------|--------|
| pending / running / failed | `mow goal run --id NAME` resumes state (plan kept) |
| done | `mow goal reset --id NAME` then `run --id` |

Also: `mow goal delete --id NAME`.

### `ext/review` (code review + security review)

Two subcommands over one read-only workflow — `mow review` (code review) and
`mow sec` (security review). Full reference: [review.md](review.md).

```bash
mow review                                  # uncommitted work
mow review --diff main...HEAD --format json
mow sec ./internal/auth --fail-on high
mow sec --format sarif --output sec.sarif   # code scanning
```

| Aspect | Behaviour |
|--------|-----------|
| Passes | 1 discovery → 2 verification; both `ReadOnly` + `Ephemeral` |
| Safety | write/shell forced off regardless of config; no session |
| Scope | `--diff` → `--staged` → `--base` → paths → dirty worktree → tree |
| Formats | `text`, `json`, `jsonl`, `sarif` (SARIF 2.1.0) |
| Exit | 0 clean · 1 findings at/above `--fail-on` · 2 error (`--exit-zero` for advisory CI) |

Findings are validated against the resolved scope before rendering (paths
normalized, lines clamped to real file length, out-of-scope and duplicate
findings dropped with a reason), so a hallucinated citation cannot reach the
report. Unverified candidates are suppressed unless `--include-unverified`.

### `ext/job`

**Inline** (no schedule file — same idea as `mow goal run --goal …`):

```bash
mow job --every 10m --prompt "Summarize git status" --allow-shell
mow job --every 1h --goal fix-ci --allow-write --allow-shell
mow job --cron "0 9 * * 1-5" --prompt "Morning brief"
# ctrl+c to stop; first --every tick fires immediately
```

**File / config** (`$MOW_HOME/job/schedules.yaml` or `extensions.job`):

```yaml
schedules:
  - id: hourly
    every: 1h                 # Go duration
    goal: fix-ci
  - id: weekday-morning
    cron: "0 9 * * 1-5"       # min hour dom month dow (local)
    prompt: "Summarize open PRs"
```

```bash
mow job                       # daemon from schedules file / extensions.job
mow job list                  # table of schedules + next fire
mow job check                 # validate; exit 1 if any bad
mow job --schedules path.yaml
```

Same id never overlaps a previous tick (skip if still running). Not HA — use host cron for production redundancy.

### `ext/proc` — background processes (general)

`proc_start` / `proc_status` / `proc_stop` tools + a `mow proc` CLI let an agent
launch a long-lived process (dev server, watcher, mock) and **keep working while
it runs** — start returns a pid immediately and the process is detached (new
session, released), logging to a file. Available anywhere (run/tty/host), gated
by `--allow-shell` (it runs shell commands). Storage: `$MOW_HOME/proc/<project>/`.

```bash
# the agent (with --allow-shell) calls proc_start, then proc_status/proc_stop:
mow run --allow-shell -p "Start the dev server (npm run dev) and confirm it responds on :3000"
# manage out-of-band:
mow proc list
mow proc logs <id> [lines]
mow proc stop <id>        #  or: mow proc stop-all
```

Do **not** use bare `bash &` for servers — the `bash` tool runs in its own
process group and kills it on return, so backgrounded children die. `proc_*` is
the supported way. Shares the mechanism with `ext/goal`'s goal-scoped
`goal_process_*` (both use `internal/proc`).

**Lifecycle:** processes are **auto-killed when the session exits** —
`Engine.Close()` (deferred by `mow run`/`tty` and called by embedders on exit)
stops everything `proc_start` launched, so nothing leaks. Pass `keep: true` to a
`proc_start` call to let that process survive session exit (nohup-like). A
crashed process skips cleanup; `mow proc stop-all` recovers.

### `ext/mcp` — both MCP directions (client tools + `mow mcp` server)

Like `ext/acp` (which serves `mow acp` **and** provides the `acp_delegate`
client tool), `ext/mcp` is one protocol pack covering both directions:

- **Client (config-driven):** connect out to trusted MCP servers (stdio or
  streamable HTTP) and register *their* tools onto the agent, name-prefixed
  `mcp_<server>_<tool>`. Activated by `extensions.mcp` / `mcp.json` — no
  subcommand. See below.
- **Server (`mow mcp`):** run mow *as* an MCP server over stdio, exposing one
  tool, `mow_prompt`, so another agent or editor (Claude Desktop, etc.) can call
  mow as a delegated sub-agent. `initialize` / `tools/list` / `tools/call` over
  newline-delimited JSON-RPC 2.0. `mow_prompt` takes `{prompt, read_only?}` and
  returns the agent's final answer as text; the serving process's own
  permissions (`--allow-write` / `--allow-shell`, plus per-call `read_only`)
  bound what a delegated run may do.

Remove the one blank import → both the `mow mcp` command and the client tools
disappear.

### `ext/mcp` / `ext/lsp` (config, opt-in)

Both are **linked in stock `cmd/mow`**. They register tools only when configured (no config → no tools, no process spawn).

Prefer **extensions.*** in config (also `$MOW_HOME/config.yaml`); file fallbacks still work (`$MOW_HOME/mcp.json` or `mcp.yaml`, `$MOW_HOME/lsp.yaml`).

MCP servers use the ecosystem-standard `mcpServers` map (the same shape as
Claude Desktop / Claude Code / Cursor / VS Code — paste an existing config
straight in):

```yaml
extensions:
  mcp:
    mcpServers:
      fs:
        command: npx
        args: ["-y", "@modelcontextprotocol/server-filesystem", "/tmp"]
      remote:
        url: https://mcp.example/mcp
  lsp:
    command: gopls
    args: [serve]
    root: .
```

The `$MOW_HOME/mcp.json` fallback takes the standard JSON directly:

```json
{ "mcpServers": { "fs": { "command": "npx", "args": ["-y", "srv"] } } }
```

The older `servers:` list form (each entry with its own `name:`) is still
accepted.

- MCP tools: `mcp_<server>_<tool>`
  - stdio: `command` + `args` (reconnect once on failure)
  - HTTP: `url:` Streamable HTTP POST (JSON or SSE body); optional `headers`
  - HTTP auth: `bearer`, `oauth2_client_credentials`, `oauth2_device_code` (RFC 8628), or `oauth2_auth_code` (loopback browser callback; `MOW_MCP_AUTH_CODE` for tests). 401 clears cache and retries once.
- LSP tools: `lsp_hover`, `lsp_definition` (reconnect once)
- LSP post-edit diagnostics: when the pack is configured, a PostTool hook pulls
  `textDocument/diagnostic` after a successful `write`/`edit` on a source file,
  appends the findings to the tool result the model sees, and emits
  `harness.lsp.diagnostics`. Diagnostics for the edit come back *with* the edit,
  so the model does not spend a turn running tests to learn it broke the build.
  Findings are sorted most severe first and then capped at
  `mow.MaxLSPDiagnostics`, so truncation can never hide an error behind a pile
  of hints. Each pull has its own 10s deadline: a server that is down, silent,
  or wedged returns the original tool result unchanged and never fails an edit
  that already succeeded. No config → no process → no event.

  Payload (`mow.Diagnostic`): `severity` (`error`|`warning`|`information`|
  `hint`), `message`, `line` (1-based), `column` (1-based, `0` when the server
  omits it), `source` (producing tool, empty when absent). `count` is the
  server total and may exceed `len(diagnostics)` after truncation.

```yaml
extensions:
  mcp:
    mcpServers:
      remote:
        url: http://127.0.0.1:3000/mcp
        headers:
          X-Custom: value
        auth:
          type: bearer
          token: "…"
      oauth-remote:
        url: https://mcp.example/mcp
        auth:
          type: oauth2_client_credentials
          token_url: https://auth.example/oauth/token
          client_id: …
          client_secret: …
          scope: mcp
```

ACP agent supports **terminal/** PTY methods when `-allow-shell` (`create` / `output` / `write` / `resize` / `wait_for_exit` / `kill` / `release`). Create returns `streaming: true`; live bytes and exit also push as `session/update` with `sessionUpdate` `terminal_output` / `terminal_exit` (clients may still poll `terminal/output`).
### Hashline edit

`tools.hashline: true` in config makes `read` emit `N:hash|line` and `edit` accept `line_hash` + `new_string`.

---

## In core (shipped)

| Capability | Why core |
|------------|----------|
| Single agent loop | Product essence |
| Session JSONL + resume | Continuity |
| Workspace path policy | Security must not be optional |
| Default-deny write/shell | Secure by default |
| `read`, `glob`, `grep` (+ opt-in write/edit/bash) | Minimal useful coding agent |
| LLM HTTP (openai + anthropic) + stream | Pluggable endpoint |
| Config yaml + env + skills | Operator ergonomics |
| AGENTS.md / CLAUDE.md load | Project instructions |
| Soft context compaction | On by default; auto-scales to `context_window × compact_ratio` (default 0.8); tool results capped (`max_tool_result_chars` ~24k) |
| Media side-lanes when configured | `generate_*` / `understand_*` |

Not core: RPC, ACP, MCP, LSP, job, goals — **packs or hosts**.

---

## Media lanes (generate / understand)

**Principle:** the agent loop is always **chat** (`llm.model`). Media is **side-lane HTTP** on separate model ids (same `base_url` / key as chat unless overridden).

| Tool | Config | HTTP shape | I/O |
|------|--------|------------|-----|
| `generate_image` | `llm.generate.image` | `POST /v1/images/generations` | → `media/image-*.png` |
| `generate_speech` | `llm.generate.speech` | `POST /v1/audio/speech` | → `media/speech-*.mp3` |
| `generate_video` | `llm.generate.video` | `POST /v1/videos/generations` + poll `GET /v1/videos/{id}` | → `media/video-*.mp4` (or job JSON) |
| `understand_image` | `llm.understand.image` | chat + image parts | path → text |
| `understand_voice` | `llm.understand.voice` | `POST /v1/audio/transcriptions` | path → text |
| `understand_video` | `llm.understand.video` | chat + video parts | path → text |

```yaml
llm:
  model: gpt-5-mini
  generate:
    image: gpt-image-1
  understand:
    image: gpt-5
    voice: whisper-1
tools:
  enable:
    - read
    - glob
    - grep
    - generate_image
    - understand_image
    - understand_voice
```

**Filesystem as interaction surface:** generate writes under `media/` (override `path`); understand only reads workspace paths and returns text. Enabling `generate_*` in `tools.enable` is itself the write opt-in for those tools — they write (workspace-jailed) without `--allow-write`. Chat model need not be multimodal. Tool results use stable lines `path:` / `bytes:` / `model:` for chaining.

Media models: yaml `llm.generate.*` / `llm.understand.*` (and `tools.enable`). Optional `llm.generate.speech_voice` for default TTS voice_id.

---

## ACP (`ext/acp`)

[Agent Client Protocol](https://agentclientprotocol.com) (JSON-RPC 2.0) — open standard for editors ↔ agents.

| Mode | How | Role |
|------|-----|------|
| **Agent** | `mow acp` | Editor/client → mow `Engine` |
| **Client / delegate** | tool `acp_delegate` | mow → peer harness subprocess |

Agent methods (Zed-oriented): `initialize`, `authenticate`, `logout`, `session/new|load|resume|list|delete|close`, `session/prompt`, `session/cancel`, `session/set_mode` (`ask` \| `code`), `session/set_config_option` (mode + model), streaming `session/update` (`agent_message_chunk`, `current_mode_update`, `config_option_update`, `terminal_output` / `terminal_exit`), `session/request_permission` (auto-allow), **fs/** read/write (workspace jail), **terminal/** create|output|write|resize|wait_for_exit|kill|release when shell allowed.

**Prompt content:** text, image, audio, resource, resource_link. Media is written under `media/acp/` and referenced in the text prompt (`promptCapabilities.image|audio|embeddedContext`).

**Modes:** `ask` = read-only tools (no write/edit/bash, no terminal); `code` = full access per process policy (`--allow-write` / `--allow-shell`). Advertised both as ACP `modes` and as a `configOptions` select (`category: mode`) for editor UIs.

**Model picker:** `configOptions` entry `id=model` (`category: model`) lists `GET /models` for the configured endpoint. Plain OpenAI-compatible catalogs (OpenAI, DeepSeek, local servers, …) show every id; optional gateway `wire` metadata filters to chat wires only and is applied on switch via `SetModelWithWire` (not shown in labels). When the gateway advertises `facet`, only empty/`chat` rows are offered (non-chat clones such as `search` / `image` stay out of the picker). Filtering uses the `facet` field only — never by parsing `:` in the model id (colons can be part of legitimate provider ids).

**Effort / thought level:** `llm.effort` / `MOW_EFFORT` / `--effort` / ACP `id=effort` (`category: thought_level`). Effort is **never** part of the model id. Gateways that extend `GET /v1/models` may advertise per-model `efforts` and `default_effort`; mow’s ACP selector uses that list (hide when empty or a single fixed tier). On chat calls mow sends body `reasoning_effort` when the catalog lists efforts (gateway maps upstream tier). Without catalog efforts, fallback is static none|low|medium|high with optional `thinking_budget` for Gemini-family models. Legacy config ids like `gemini-2.5-flash-medium` normalize to lean `model` + `effort`. Peer `extensions.acp.agents[].effort` may inject `--reasoning-effort` when the peer CLI accepts that flag.

**Why ACP for delegate (not a private RPC):** same wire for editors and peer agents; avoid inventing mow↔claude, mow↔codex one-offs. Core stays **one loop**; delegation is a tool with workspace jail + timeout.

**Delegate v2:** peer process + ACP session are **reused** across `acp_delegate` calls (same agent + cwd) until idle TTL or death. While the peer runs, the parent Engine emits:
- `harness.delegate.chunk` — peer **answer** text (`agent_message_chunk`)
- `harness.delegate.progress` — peer **status** (tool_call / thought; not part of the tool result)

CLI (`mow run --stream`) prints chunks on stderr and progress as `↳ agent: …`. `mow acp` forwards both to the editor as `session/update` (message + thought). Tool result still returns the full concatenated answer only.

**OnEvent fan-out:** `Engine.AddOnEvent` registers additional listeners; `SetOnEvent` replaces all. `mow rpc` uses `AddOnEvent` so a host can keep its own listener on the same Engine.

```text
Editor ──ACP──▶ mow acp ──Engine──▶ LLM
mow loop ──acp_delegate──▶ peer ACP agent (other harness)
```

### RPC control plane (`ext/rpc`)

**JSON-RPC 2.0** over line-delimited JSON on stdio. Methods: `prompt`, `cancel`, `status`, `session`, `version`, `ping`. Responses and notifications carry `"jsonrpc":"2.0"` and errors carry a standard `code` (-32601 method not found, -32600 invalid request, -32700 parse error, -32603 internal). Requests may include `"jsonrpc":"2.0"` but need not — minimal clients sending only `id`/`method`/`params` still work.  
During `prompt`, server may write notifications `{"jsonrpc":"2.0","method":"event","params":{…Event}}` (`loop.run.start`, `loop.token`, `loop.reasoning`, `harness.tool.start`, `harness.tool.end`, `loop.turn`, `harness.delegate.chunk`, `loop.run.end`). Final response includes `run_id` and `stop_reason`.

`tool.end` includes `duration_ms` (wall time for that tool). Tool batches may run up to `policy.max_parallel_tools` concurrent Exec calls (default 8); soft results append in call order. See [harness.md](harness.md) § Abort / cancel.
---

## Custom tools

Stock mow has no demo tools. In a custom binary:

```go
func init() {
    ext.RegisterTool(myTool{}) // or blank-import a pack that registers tools
}
```

### Hooks (lifecycle)

Extensions (or external adapters) register hooks — enough surface for
context-optimizer patterns (deny/rewrite tools, compress results, inject
system text) without a product-specific pack in core.

Order in `Engine.Prompt`:

```text
OnSessionStart          // once in New (system/skills already loaded)
OnUserPrompt            // each Prompt
  [OnPreCompact?]       // before each LLM call when MaxContextChars set
  LLM → OnAfterTurn
  for each tool: OnPreTool → Exec → OnPostTool
OnStop                  // after Prompt returns (success or error)
```

| Register | Can |
|----------|-----|
| `RegisterSessionStart` | Append system text for the Engine lifetime |
| `RegisterUserPrompt` | Rewrite user text; append system for this turn |
| `RegisterPreCompact` | Skip compaction or supply summary stub (`MessageCount` on ext event; full `Messages` on `mow.Options.OnPreCompact`) |
| `RegisterPreTool` | Deny, rewrite args, add context on the tool result |
| `RegisterPostTool` | Rewrite tool result the model sees |
| `RegisterAfterTurn` | Observe assistant text / tool-call turns |
| `RegisterStop` | Observe final text / error |

```go
ext.RegisterPreTool(func(ctx context.Context, e ext.PreToolEvent) (ext.PreToolDecision, error) {
    // e.g. route large-output tools, rewrite paths, deny dangerous calls
    // Deny: true → tool result error for the model; return err → abort Prompt
    return ext.PreToolDecision{}, nil
})
ext.RegisterPostTool(func(ctx context.Context, e ext.PostToolEvent) (ext.PostToolDecision, error) {
    // e.g. truncate / summarize large results before they hit the model
    return ext.PostToolDecision{Rewrite: true, Result: summarize(e.Result)}, nil
})
// Or pass Hooks in mow.Options / eng.AddPreTool / eng.AddPostTool
```

No `ext/contextmode` pack is required: wire any MCP or local optimizer to these hooks.

---

## Goals, MCP, sub-agents, LSP (stance)

| Feature | Core? | Recommendation |
|---------|-------|----------------|
| **Goals** | **No** | Host/UI or session events |
| **MCP** | **No** | Pack that `RegisterTool`s from servers |
| **Sub-agents** | **No separate feature** | Same as **named agents** via `acp_delegate` (`agent` / alias `subagent`). Prefer `mow_agents` for multi-model mow; full `agents[]` for external tools |
| **LSP / DAP** | **No** | Tool pack or via MCP |
| **Browser / sandbox** | **No** | High risk; deploy-specific packs |

Priority for new packs: deepen ACP as needed → MCP → LSP → goals only if many UIs share one store.

---

## Optional attribution labels (`X-Mow-*`)

Optional **labels only** — not routing (routing = path + body `model`). Plain OpenAI/Anthropic endpoints ignore them. A gateway may accept these as aliases into its own attribution slots (actor / session / component).

| Header | Meaning |
|--------|---------|
| `X-Mow-Actor` | Who (`mow`, …) |
| `X-Mow-Session` | Session id |
| `X-Mow-Component` | `turn.chat`, `tool.generate_image`, … |

Constants: `internal/llm.HeaderActor` / `HeaderSession` / `HeaderComponent`.

---

## Feature menu (later packs)

mow stays minimal by default. Heavier features (hashline edit, DAP, memory, browser sandbox, …) belong as optional packs when needed — not core checklist items.

**Out of scope for mow core:** multi-provider catalog, OAuth credential brokering, channel delivery, rich host UI chrome. Hosts and gateways own those.

---

## Skills (config-only)

Each skill is a folder with a `SKILL.md` entry point, one level under
`skills.dirs`, `$MOW_HOME/skills` (default `~/.mow/skills`), or trusted
`workspace/.mow/skills` — e.g. `~/.mow/skills/humanizer/SKILL.md`. Clone a skill
repo straight in; other files in the folder are ignored.  
Project config/skills require `mow trust` (out-of-band, `$MOW_HOME/trusted`) or `MOW_TRUST_PROJECT=1`.

---

## Command hooks (`cmdhook`) — Claude Code plugin bridge

`cmdhook` runs Claude Code-style command hooks (a plugin `hooks.json`) against
mow's hook system, so existing plugins — e.g. [context-mode](https://github.com/mksglu/context-mode) —
work unchanged. Each hook command receives the event as JSON on stdin and may
return a decision as JSON on stdout (or block via exit code 2 with stderr as
the reason), exactly as under Claude Code.

Config (`extensions.cmdhook`, or `$MOW_HOME/cmdhook.yaml`):

```yaml
extensions:
  cmdhook:
    root: /path/to/plugin        # ${CLAUDE_PLUGIN_ROOT}
    hooks_file: hooks/hooks.json # default, relative to root
    timeout_sec: 10              # per command
```

Event mapping (Claude → mow hook):

| Claude event | mow hook | Decision honored |
|--------------|----------|------------------|
| `PreToolUse` | PreTool | `permissionDecision` deny/ask → deny; `additionalContext`; `updatedInput` → rewrite args |
| `PostToolUse` | PostTool | `additionalContext` appended to the tool result |
| `UserPromptSubmit` | UserPrompt | stdout/`additionalContext` → system append; block → abort |
| `SessionStart` | SessionStart | `additionalContext` → system append |
| `Stop` | Stop | fire-and-forget capture |
| `PreCompact` | PreCompact | fire-and-forget |

Tool names are translated to Claude conventions for matchers and payloads
(`read`→`Read`, `mcp_srv_x`→`mcp__srv_x`). mow's engine has no interactive
prompt, so a PreToolUse `ask` is treated as deny — hosts with an approval UI
gate power tools themselves.

Same trust bar as any executable extension: the commands run with full machine
access. Link the pack only for plugins you trust.

---

## Security notes for packs

- Project-local executable extensions = full machine trust (same bar as shell).  
- Prefer compiled-in packs or explicit trust.  
- FS tools stay workspace-jailed.  
- MCP / browser / DAP: default **off**.  
- `acp_delegate` peers: timeout + cwd jail; do not inherit unrestricted shell by default.

---

## See also

- [architecture.md](architecture.md)  
- [harness.md](harness.md)  
- [agentclientprotocol.com](https://agentclientprotocol.com) — ACP  
